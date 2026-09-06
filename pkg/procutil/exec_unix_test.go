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
