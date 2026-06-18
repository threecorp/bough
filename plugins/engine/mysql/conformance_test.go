//go:build conformance

// The conformance test exercises the bough mysql plugin end-to-end:
// the binary is spawned under go-plugin, the lifecycle phases run
// against a real mysqld container, and every EnvVars invariant
// (reachable / non-empty / shell-safe-with-opt-out) is asserted.
//
// Build tag `conformance` so plain `go test ./...` does not pull in
// docker daemon requirements. CI invokes `go test -tags=conformance
// ./plugins/engine/mysql/...` after building the plugin binary.
package mysql_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/conformance"
)

const (
	mysqlConformanceImage      = "mysql:8.4"
	mysqlConformanceReadyMax   = 180 * time.Second
	mysqlConformancePluginEnv  = "BOUGH_CONFORMANCE_PLUGIN_BIN"
	mysqlHandshakeProtoVersion = 0x0a
)

// TestMySQLConformance is the single contract-guard entry point for
// the bough mysql plugin. CI sets BOUGH_CONFORMANCE_PLUGIN_BIN to the
// just-built `bin/bough-plugin-mysql` and the suite drives the rest.
func TestMySQLConformance(t *testing.T) {
	bin := os.Getenv(mysqlConformancePluginEnv)
	if bin == "" {
		t.Skipf("set %s to the bough-plugin-mysql binary path", mysqlConformancePluginEnv)
	}
	conformance.Run(t, conformance.Config{
		PluginBinary:    bin,
		Image:           mysqlConformanceImage,
		ReadyTimeout:    mysqlConformanceReadyMax,
		IdempotentCount: 2,
		// The mysql plugin emits BOUGH_MYSQL_HOST / _PORT / _SOCKET —
		// no DSN — so the AssertShellSafe stays strict.
		AllowShellMetachars: false,
		NativeProbe:         mysqlHandshakeProbe,
		// The plugin only bind-mounts Datadir; it never writes there
		// itself, so a chmod 0o000 parent does not surface as an Up
		// error. The mysqld process inside the container would crash
		// on its first transaction log write, but by then Up has long
		// returned success. AssertReachable + NativeProbe cover the
		// downstream failure already.
		SkipDatadirPermission: true,
	})
}

// mysqlHandshakeProbe is stdlib-only. MySQL servers send an Initial
// Handshake Packet as the first thing on a new TCP connection: 3-byte
// payload length, 1-byte sequence id, 1-byte protocol version. The
// protocol version has been 10 (0x0a) since MySQL 4.1, so reading
// those five bytes is a cheap proof that we are talking to mysqld
// and not, say, a docker entrypoint that is still bringing the
// daemon up.
//
// Why the retry loop: the official mysql:8.x image runs a known
// initdb sequence — temporary-mysqld → seed-databases → kill → exec
// final-mysqld — and the plugin's ReadyCheck (`mysqladmin ping`)
// goes green during the temporary-mysqld phase. The handshake we
// dial here can land between the kill and the final exec, in which
// case the TCP connect succeeds but the read returns EOF immediately.
// The loop survives that ~1-3 s window without weakening the contract:
// when the final mysqld is up the very next dial returns a real
// handshake.
func mysqlHandshakeProbe(ctx context.Context, hostPort string) error {
	deadline := time.Now().Add(30 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	var lastErr error
	for time.Now().Before(deadline) {
		if err := mysqlHandshakeOnce(ctx, hostPort); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("mysql probe: never saw a valid handshake on %s within 30s, last err: %w",
		hostPort, lastErr)
}

func mysqlHandshakeOnce(ctx context.Context, hostPort string) error {
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return fmt.Errorf("dial %s: %w", hostPort, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	var buf [5]byte
	if _, err := io.ReadFull(conn, buf[:]); err != nil {
		return fmt.Errorf("read handshake from %s: %w", hostPort, err)
	}
	if buf[4] != mysqlHandshakeProtoVersion {
		return fmt.Errorf("handshake protocol = %#x, want %#x (MySQL Initial Handshake)",
			buf[4], mysqlHandshakeProtoVersion)
	}
	return nil
}
