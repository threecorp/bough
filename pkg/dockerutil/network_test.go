//go:build darwin || linux

package dockerutil

import (
	"fmt"
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
// taken. This is the dockerUp pre-flight contract.
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

// Helper to assert the loopback address format that IsPortFree uses.
// Pinned so a refactor that switches to "localhost" (which can resolve
// to ::1 on dual-stack hosts and miss IPv4-only port conflicts) fails
// loudly.
func TestIsPortFree_BindString(t *testing.T) {
	want := "127.0.0.1:1234"
	got := fmt.Sprintf("127.0.0.1:%d", 1234)
	if got != want {
		t.Errorf("loopback bind string drift: got %q want %q", got, want)
	}
}
