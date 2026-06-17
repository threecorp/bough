package conformance

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// RedisPing dials hostPort, sends a RESP PING, and waits for `+PONG`.
// Stdlib-only so a plugin author can wire it up without taking a
// dependency on a redis client library.
//
// Plugin authors who prefer go-redis can ignore this helper entirely
// and pass their own NativeProbe — the suite is happy with any func
// that returns a non-nil error on protocol-level unreachability.
func RedisPing(ctx context.Context, hostPort string) error {
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return fmt.Errorf("redis probe: dial %s: %w", hostPort, err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	}
	// RESP inline command form: `PING\r\n`. Servers respond `+PONG\r\n`.
	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		return fmt.Errorf("redis probe: write PING: %w", err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("redis probe: read reply: %w", err)
	}
	if !strings.HasPrefix(reply, "+PONG") {
		return fmt.Errorf("redis probe: want +PONG, got %q", strings.TrimSpace(reply))
	}
	return nil
}

// ElasticsearchGetRoot issues `GET /` against
// http://<hostPort>/ and requires a 200 response. ES returns 200 on
// `/` once the cluster is yellow-or-better; single-node clusters are
// always yellow, so a green response is the canonical "ready for
// queries" signal that no sniff-aware client can dispute.
//
// Why this matters: v0.2.6 shipped because nothing tested that an
// HTTP client running on the host could actually reach the bough-
// allocated port. AssertReachable proves the TCP socket is open;
// this probe proves the protocol round-trip works end-to-end.
func ElasticsearchGetRoot(ctx context.Context, hostPort string) error {
	url := "http://" + hostPort + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("elasticsearch probe: new request: %w", err)
	}
	cli := &http.Client{Timeout: 5 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return fmt.Errorf("elasticsearch probe: GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("elasticsearch probe: GET %s status=%d, want 200", url, resp.StatusCode)
	}
	return nil
}
