//go:build darwin || linux

package procutil

import (
	"net"
	"os"
	"os/exec"
	"testing"
)

func TestLsofListener_FindsListenerPID(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not on PATH")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	if got, want := LsofListener(port), os.Getpid(); got != want {
		t.Errorf("LsofListener(%d) = %d, want this test process's pid %d", port, got, want)
	}
}

func TestLsofListener_ZeroWhenNothingListening(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not on PATH")
	}
	// Grab a kernel-assigned port then release it so nothing is listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	if got := LsofListener(port); got != 0 {
		t.Errorf("LsofListener(%d) = %d, want 0 (nothing listening)", port, got)
	}
}

// TestKillStrayProcessCompose_NoMatchIsSafe is a smoke test: with a
// bogus cwd prefix nothing can match, so the call must be a harmless
// no-op (exercising the pgrep + lsof parsing path without signalling any
// real process). A fully hermetic test would need a live process-compose
// under a known cwd, which is left to the conformance / integration
// suite.
func TestKillStrayProcessCompose_NoMatchIsSafe(t *testing.T) {
	KillStrayProcessCompose("/nonexistent-bough-procutil-test-prefix-xyz")
}
