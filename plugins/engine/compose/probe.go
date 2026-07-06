//go:build darwin || linux

package compose

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"time"
)

// ReadyCheck polls the given port until it responds or timeoutSec
// elapses. Which protocol probe to use comes from the cached upState
// (extras["compose.ready_probe"], defaulting to a plain TCP dial when
// unset or when Up() never ran in this process) — see the package doc
// for why a generic compose wrapper cannot know the wrapped service's
// protocol upfront the way the four bundled plugins do.
func (p *Provider) ReadyCheck(ctx context.Context, ports []int, timeoutSec int) (bool, error) {
	port := firstListenPort(ports)
	if port <= 0 {
		return false, fmt.Errorf("compose: ReadyCheck: invalid ports %v", ports)
	}
	probe := "tcp"
	if st := p.cachedState(port); st != nil && st.ReadyProbe != "" {
		probe = st.ReadyProbe
	}
	if timeoutSec <= 0 {
		timeoutSec = 600
	}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for {
		ok, err := probeOnce(ctx, probe, port)
		if ok {
			return true, nil
		}
		if isUnknownProbe(err) {
			return false, err
		}
		if !time.Now().Before(deadline) {
			return false, fmt.Errorf("compose: ReadyCheck: port %d not ready within %ds (probe=%s): %w", port, timeoutSec, probe, err)
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// firstListenPort returns ports[0], or 0 when ports is empty —
// mirrors the same helper in the redis/mysql/postgres plugins.
func firstListenPort(ports []int) int {
	if len(ports) > 0 {
		return ports[0]
	}
	return 0
}

type unknownProbeError struct{ name string }

func (e *unknownProbeError) Error() string {
	return fmt.Sprintf("compose: unknown compose.ready_probe %q (want tcp, redis, postgres, or http)", e.name)
}

func isUnknownProbe(err error) bool {
	_, ok := err.(*unknownProbeError)
	return ok
}

// probeOnce dials 127.0.0.1:port once and returns whether the named
// probe considers it ready. A connection/protocol failure is returned
// as a non-nil error for logging context, not as a fatal condition —
// ReadyCheck's retry loop treats any error other than
// *unknownProbeError as "not ready yet, try again."
func probeOnce(ctx context.Context, probe string, port int) (bool, error) {
	switch probe {
	case "tcp":
		return tcpProbe(ctx, port)
	case "redis":
		return redisProbe(ctx, port)
	case "postgres":
		return postgresProbe(ctx, port)
	case "http":
		return httpProbe(ctx, port)
	default:
		return false, &unknownProbeError{name: probe}
	}
}

func dial(ctx context.Context, port int) (net.Conn, error) {
	d := net.Dialer{Timeout: 2 * time.Second}
	return d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

func tcpProbe(ctx context.Context, port int) (bool, error) {
	conn, err := dial(ctx, port)
	if err != nil {
		return false, err
	}
	_ = conn.Close()
	return true, nil
}

// redisProbe sends a bare RESP inline PING and expects "+PONG". This
// is deliberately minimal (no RESP3/AUTH handling) — an operator
// whose compose-wrapped redis needs auth to even PING should keep the
// "tcp" default rather than opt into this probe.
func redisProbe(ctx context.Context, port int) (bool, error) {
	conn, err := dial(ctx, port)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		return false, err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return false, err
	}
	return reply == "+PONG\r\n", nil
}

// postgresProbe writes the 8-byte SSLRequest packet (len=8, code
// 80877103) and reads a single byte: 'S' or 'N' both confirm a real
// postgres wire-protocol listener answered (accept/decline SSL is
// irrelevant to readiness, only that something speaking the protocol
// replied at all).
func postgresProbe(ctx context.Context, port int) (bool, error) {
	conn, err := dial(ctx, port)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	sslRequest := []byte{0x00, 0x00, 0x00, 0x08, 0x04, 0xd2, 0x16, 0x2f}
	if _, err := conn.Write(sslRequest); err != nil {
		return false, err
	}
	reply := make([]byte, 1)
	if _, err := conn.Read(reply); err != nil {
		return false, err
	}
	return reply[0] == 'S' || reply[0] == 'N', nil
}

// httpProbe issues a bare HTTP/1.0 GET and accepts any well-formed
// status line — readiness here means "something is serving HTTP,"
// not any particular endpoint's semantics.
func httpProbe(ctx context.Context, port int) (bool, error) {
	conn, err := dial(ctx, port)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("GET / HTTP/1.0\r\n\r\n")); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return false, err
	}
	var httpVer string
	var status int
	if _, err := fmt.Sscanf(line, "%s %d", &httpVer, &status); err != nil {
		return false, fmt.Errorf("compose: httpProbe: unparseable status line %q: %w", line, err)
	}
	return status > 0, nil
}
