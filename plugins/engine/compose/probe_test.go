//go:build darwin || linux

package compose

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeServer starts a TCP listener on 127.0.0.1 and calls handle for
// every accepted connection in its own goroutine, so probe functions
// can be exercised without a real redis/postgres/http server. It is
// closed automatically via t.Cleanup.
func fakeServer(t *testing.T, handle func(net.Conn)) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(conn)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestProbeOnce_TCP(t *testing.T) {
	port := fakeServer(t, func(c net.Conn) { _ = c.Close() })
	ok, err := probeOnce(context.Background(), "tcp", port)
	if err != nil || !ok {
		t.Errorf("probeOnce(tcp) on an open port = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestProbeOnce_TCP_ClosedPort(t *testing.T) {
	// A listener bound then immediately closed frees the port back to
	// the OS but nothing is listening on it during the probe.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	ok, err := probeOnce(context.Background(), "tcp", port)
	if ok || err == nil {
		t.Errorf("probeOnce(tcp) on a closed port = (%v, %v), want (false, non-nil)", ok, err)
	}
}

func TestProbeOnce_Redis(t *testing.T) {
	port := fakeServer(t, func(c net.Conn) {
		defer func() { _ = c.Close() }()
		buf := make([]byte, 64)
		if _, err := c.Read(buf); err != nil {
			return
		}
		_, _ = c.Write([]byte("+PONG\r\n"))
	})
	ok, err := probeOnce(context.Background(), "redis", port)
	if err != nil || !ok {
		t.Errorf("probeOnce(redis) against a +PONG server = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestProbeOnce_Redis_UnexpectedReply(t *testing.T) {
	port := fakeServer(t, func(c net.Conn) {
		defer func() { _ = c.Close() }()
		buf := make([]byte, 64)
		if _, err := c.Read(buf); err != nil {
			return
		}
		_, _ = c.Write([]byte("-ERR unknown command\r\n"))
	})
	ok, _ := probeOnce(context.Background(), "redis", port)
	if ok {
		t.Error("probeOnce(redis) against a non-PONG reply = true, want false")
	}
}

func TestProbeOnce_Postgres(t *testing.T) {
	port := fakeServer(t, func(c net.Conn) {
		defer func() { _ = c.Close() }()
		buf := make([]byte, 8)
		if _, err := c.Read(buf); err != nil {
			return
		}
		// 'S' = server accepts SSL; a real postgres wire handshake.
		_, _ = c.Write([]byte("S"))
	})
	ok, err := probeOnce(context.Background(), "postgres", port)
	if err != nil || !ok {
		t.Errorf("probeOnce(postgres) against an 'S' reply = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestProbeOnce_HTTP(t *testing.T) {
	port := fakeServer(t, func(c net.Conn) {
		defer func() { _ = c.Close() }()
		reader := bufio.NewReader(c)
		if _, err := reader.ReadString('\n'); err != nil {
			return
		}
		_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
	})
	ok, err := probeOnce(context.Background(), "http", port)
	if err != nil || !ok {
		t.Errorf("probeOnce(http) against a 200 OK = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestProbeOnce_UnknownProbeIsAnError(t *testing.T) {
	_, err := probeOnce(context.Background(), "carrier-pigeon", 1)
	if err == nil {
		t.Fatal("probeOnce with an unknown probe name = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Errorf("error %q does not name the unknown probe", err.Error())
	}
}

func TestReadyCheck_SucceedsOncePortOpens(t *testing.T) {
	p := New()
	port := fakeServer(t, func(c net.Conn) { _ = c.Close() })
	ok, err := p.ReadyCheck(context.Background(), []int{port}, 5)
	if err != nil || !ok {
		t.Errorf("ReadyCheck = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestReadyCheck_UsesCachedProbe(t *testing.T) {
	p := New()
	port := fakeServer(t, func(c net.Conn) {
		defer func() { _ = c.Close() }()
		buf := make([]byte, 64)
		if _, err := c.Read(buf); err != nil {
			return
		}
		_, _ = c.Write([]byte("+PONG\r\n"))
	})
	p.cacheState(port, &upState{ReadyProbe: "redis"})
	ok, err := p.ReadyCheck(context.Background(), []int{port}, 5)
	if err != nil || !ok {
		t.Errorf("ReadyCheck with cached redis probe = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestReadyCheck_TimesOutWhenNothingListens(t *testing.T) {
	p := New()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	start := time.Now()
	ok, err := p.ReadyCheck(context.Background(), []int{port}, 1)
	if ok || err == nil {
		t.Errorf("ReadyCheck against a closed port = (%v, %v), want (false, non-nil)", ok, err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("ReadyCheck took %v for a 1s timeout, retry loop is not honoring the deadline", elapsed)
	}
}

func TestReadyCheck_InvalidPorts(t *testing.T) {
	p := New()
	if _, err := p.ReadyCheck(context.Background(), nil, 5); err == nil {
		t.Error("ReadyCheck with no ports = nil error, want an error")
	}
}
