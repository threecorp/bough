//go:build darwin || linux

package conformance

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// fakePostgres accepts connections, reads the 8-byte SSLRequest each
// client sends, records the first one for assertion, and replies with
// the given single byte. It loops so PostgresProbe's retry can
// reconnect. Stdlib-only — no docker, no real postgres.
func fakePostgres(t *testing.T, reply byte) (addr string, firstReq func() []byte, closeFn func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	var req []byte
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				var buf [8]byte
				if _, err := io.ReadFull(conn, buf[:]); err != nil {
					return
				}
				mu.Lock()
				if req == nil {
					req = append([]byte(nil), buf[:]...)
				}
				mu.Unlock()
				// Set firstReq before replying so the client cannot read
				// the reply (and return) before req is recorded.
				_, _ = conn.Write([]byte{reply})
			}()
		}
	}()
	getReq := func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return req
	}
	return ln.Addr().String(), getReq, func() { _ = ln.Close() }
}

// TestPostgresProbe_AcceptsSAndN is the regression guard for issue #72:
// postgres gained a NativeProbe. Both valid SSLRequest replies ('S' =
// SSL offered, 'N' = declined) must pass, and the probe must have sent
// the canonical 8-byte SSLRequest packet.
func TestPostgresProbe_AcceptsSAndN(t *testing.T) {
	for _, reply := range []byte{'S', 'N'} {
		addr, firstReq, closeFn := fakePostgres(t, reply)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := PostgresProbe(ctx, addr)
		cancel()
		if err != nil {
			t.Errorf("PostgresProbe with reply %q = %v, want nil", reply, err)
		}
		// Spelled out rather than compared against pgSSLRequest: the
		// probe sends that same var, so comparing to it passed whatever
		// bytes it held. int32(80877103) = 0x04d2162f is the protocol
		// constant, prefixed by the 4-byte length 8.
		wantSSLRequest := []byte{0x00, 0x00, 0x00, 0x08, 0x04, 0xd2, 0x16, 0x2f}
		if got := firstReq(); !bytes.Equal(got, wantSSLRequest) {
			t.Errorf("probe sent %#x, want the SSLRequest %#x", got, wantSSLRequest)
		}
		closeFn()
	}
}

// TestPostgresSSLProbeOnce_RejectsGarbageReply proves the probe fails
// (rather than passing) when the peer answers with a byte that is not a
// valid SSLRequest reply — i.e. it is not actually speaking the
// postgres wire protocol.
func TestPostgresSSLProbeOnce_RejectsGarbageReply(t *testing.T) {
	addr, _, closeFn := fakePostgres(t, 'X')
	defer closeFn()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := postgresSSLProbeOnce(ctx, addr); err == nil {
		t.Errorf("postgresSSLProbeOnce with a non-'S'/'N' reply = nil, want an error")
	}
}
