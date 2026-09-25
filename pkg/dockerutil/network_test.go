//go:build darwin || linux

package dockerutil

import (
	"net"
	"testing"
)

// TestIsPortFree_FreePort grabs a kernel-assigned port (port 0), closes
// it, and verifies IsPortFree reports the port as free immediately
// after. The race between Close() and the next Listen is small enough
// in practice that the assertion is stable on every OS we target.
func TestIsPortFree_FreePort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("seed listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}
	if !IsPortFree(port) {
		t.Errorf("IsPortFree(%d) = false right after Close, want true", port)
	}
}

// TestIsPortFree_OccupiedPort opens a listener on a kernel-assigned
// port, leaves it open, and verifies IsPortFree reports the port as
// taken. This is the docker backend Up pre-flight contract, and it doubles as
// the dual-stack regression guard: IsPortFree binds "127.0.0.1:<port>"
// specifically, not "localhost:<port>" (which can resolve to ::1 on a
// dual-stack host and miss an IPv4-only conflict like the one this
// listener creates) — a refactor introducing that regression would
// make this test start failing since IsPortFree would then wrongly
// report the occupied port as free.
func TestIsPortFree_OccupiedPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("seed listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	port := l.Addr().(*net.TCPAddr).Port

	if IsPortFree(port) {
		t.Errorf("IsPortFree(%d) = true while listener is open, want false", port)
	}
}
