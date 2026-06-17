package conformance

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// newDatadir hands out a directory the conformance suite can use as
// `UpReq.Datadir` and that this package, not the testing harness,
// owns the cleanup of.
//
// Why we don't use t.TempDir(): the container's engine writes into
// the bind-mount as its own uid (mysql 999, redis 999, …). On Linux
// runners the host non-root test user cannot rm -rf the result. If
// we let testing's t.TempDir auto-cleanup run, the harness emits
// `testing.go:1369: TempDir RemoveAll cleanup: permission denied`
// and marks the parent test failed even when every sub-test passed.
//
// The fallback path uses `docker run --rm` against alpine:3.20 to
// chown the tree back to the host uid before retrying RemoveAll.
// CI pre-pulls alpine alongside the engine image; in macOS Docker
// Desktop the VirtioFS layer handles the uid mismatch so the
// fallback never triggers.
func newDatadir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bough-conformance-datadir-")
	if err != nil {
		t.Fatalf("conformance: mkdir datadir: %v", err)
	}
	t.Cleanup(func() {
		removeDatadirWithChownFallback(t, dir)
	})
	return dir
}

// removeDatadirWithChownFallback tries os.RemoveAll first; on a
// permission denied (linux only) it spins up a one-shot alpine:3.20
// container to chown the tree back to the host uid, then retries.
func removeDatadirWithChownFallback(t *testing.T, dir string) {
	t.Helper()
	if err := os.RemoveAll(dir); err == nil {
		return
	} else if !isPermissionDeniedFromContainerUID(err) {
		t.Logf("conformance datadir cleanup: %v", err)
		return
	}

	dockerBin, err := exec.LookPath("docker")
	if err != nil {
		t.Logf("conformance datadir cleanup: docker not on PATH (cannot chown stale %s)", dir)
		return
	}
	uid := os.Getuid()
	gid := os.Getgid()
	cmd := exec.Command(dockerBin, "run", "--rm", //nolint:gosec // dir comes from our own os.MkdirTemp; uid/gid are runtime ints
		"--user=0:0",
		"-v", dir+":/work",
		"alpine:3.20",
		"chown", "-R", fmt.Sprintf("%d:%d", uid, gid), "/work")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("conformance datadir cleanup: docker chown failed: %v\n%s", err, out)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Logf("conformance datadir cleanup: RemoveAll after chown still failed: %v", err)
	}
}
