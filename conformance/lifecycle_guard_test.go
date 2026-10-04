//go:build darwin || linux

package conformance

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestPortsStillAnswering_SeesALiveListener is the discriminating half of
// the Down post-condition: before it existed, a plugin whose Down did
// nothing passed every conformance sub-test, because runDownPhase only
// checked that Down returned nil.
func TestPortsStillAnswering_SeesALiveListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	still := portsStillAnswering([]int{port}, 300*time.Millisecond)
	if len(still) != 1 || still[0] != port {
		t.Fatalf("portsStillAnswering = %v, want [%d] for a port that is still bound", still, port)
	}
}

// TestPortsStillAnswering_ClosedPortReturnsAtOnce keeps the common path
// free: a plugin that really stopped its engine must not pay the whole
// release window on every Down.
func TestPortsStillAnswering_ClosedPortReturnsAtOnce(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	start := time.Now()
	if still := portsStillAnswering([]int{port}, time.Minute); len(still) != 0 {
		t.Fatalf("portsStillAnswering = %v, want empty for a closed port", still)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v on a closed port; the wait window should not be spent when nothing answers", d)
	}
}

// TestPortsStillAnswering_SkipsNonPorts guards the zero/negative entries
// a registry or a multi-role plugin can hand over.
func TestPortsStillAnswering_SkipsNonPorts(t *testing.T) {
	if still := portsStillAnswering([]int{0, -1}, 10*time.Millisecond); len(still) != 0 {
		t.Fatalf("portsStillAnswering = %v, want empty", still)
	}
}

// TestIsPermissionDeniedFromContainerUID_OnlyFilesystemErrors pins the
// narrowing: the check turns a Cleanup failure into a Skip that the
// parent test still reports as a pass, so it must recognise ONLY the
// container-uid case it was written for. A bare substring match on
// "permission denied" also swallowed a docker-socket permission error
// and any plugin bug whose message happened to carry the phrase.
func TestIsPermissionDeniedFromContainerUID_OnlyFilesystemErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			"wrapped RemoveAll PathError (the case this exists for)",
			fmt.Errorf("mysql: Cleanup: %w", &fs.PathError{Op: "unlinkat", Path: "/d/ibdata1", Err: syscall.EACCES}),
			true,
		},
		{
			"PathError rendered with %v, chain dropped",
			errors.New("mysql: cleanup /d: unlinkat /d/ibdata1: permission denied"),
			true,
		},
		{
			// os.RemoveAll's other ops, as they arrive over the plugin
			// RPC: the client rebuilds the error with errors.New, so only
			// the text is left to match.
			"openfdat over RPC (non-empty container-owned dir on Linux)",
			errors.New("mysql: cleanup /d: openfdat /d/mysql: permission denied"),
			true,
		},
		{
			"readdirnames over RPC",
			errors.New("redis: cleanup /d: readdirnames /d/appendonlydir: permission denied"),
			true,
		},
		{
			"docker socket permission error must NOT skip",
			errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock: permission denied"),
			false,
		},
		{
			"plugin bug that merely mentions the phrase must NOT skip",
			errors.New("redis: Cleanup: refusing to run: config says permission denied"),
			false,
		},
		{
			"an unrelated filesystem error must NOT skip",
			&fs.PathError{Op: "unlinkat", Path: "/d", Err: syscall.ENOTEMPTY},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPermissionDeniedFromContainerUID(tc.err); got != tc.want {
				t.Errorf("isPermissionDeniedFromContainerUID(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsPermissionDeniedFromContainerUID_RealRemoveAll proves the real
// os.RemoveAll error shape is still recognised, so narrowing the check
// did not turn the Linux-runner case it exists for into a hard failure.
// Skipped when running as root, which can unlink regardless.
func TestIsPermissionDeniedFromContainerUID_RealRemoveAll(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: the unlink cannot be made to fail this way")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "child"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Clearing write+execute on the parent is what makes unlinkat of the
	// child fail with EACCES — the same shape as a container-written
	// datadir the host user cannot unlink.
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	err := os.RemoveAll(locked)
	if err == nil {
		t.Skip("this filesystem allowed the removal anyway")
	}
	if !isPermissionDeniedFromContainerUID(err) {
		t.Errorf("real os.RemoveAll permission error not recognised: %#v", err)
	}
}
