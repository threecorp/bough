package cli

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestWaitGone covers the SIGTERM→SIGKILL escalation gate added in
// v0.9.9: stop must be able to tell whether a signalled daemon has
// actually exited before it reports success.
func TestWaitGone(t *testing.T) {
	// our own process is alive → waitGone must report not-gone
	if waitGone(os.Getpid(), 200*time.Millisecond) {
		t.Errorf("waitGone(self) = true, want false (this process is alive)")
	}
	// an almost-certainly-absent pid → gone
	if !waitGone(2147483646, 200*time.Millisecond) {
		t.Errorf("waitGone(absent pid) = false, want true")
	}
}

// TestParseDaemonPID covers the fallback that lets `observer stop` /
// `status` find a live daemon when the pid file is stale or missing —
// the regression behind #45 (a stale observer.pid hid a running
// daemon, so stop reported "not running" and orphaned it).
func TestParseDaemonPID(t *testing.T) {
	root := "/Users/x/src/claude"
	ps := strings.Join([]string{
		"  100 /usr/bin/some-other-process --root /Users/x/src/claude",
		"  200 /Users/x/.local/bin/bough observer _run-daemon --root /Users/x/src/claude --interval 3600",
		"  300 /Users/x/.local/bin/bough observer _run-daemon --root /Users/x/src/other --interval 600",
	}, "\n")
	alive := func(int) bool { return true }
	dead := func(int) bool { return false }

	// matches the daemon line for the right root (not the other root,
	// and not the non-daemon process that merely shares the --root arg)
	if pid, ok := parseDaemonPID(ps, root, 1, alive); !ok || pid != 200 {
		t.Fatalf("got (%d,%v) want (200,true)", pid, ok)
	}

	// skips our own pid so `stop` never signals itself
	if _, ok := parseDaemonPID(ps, root, 200, alive); ok {
		t.Errorf("should skip self pid 200")
	}

	// a matching line whose process is dead is not returned
	if _, ok := parseDaemonPID(ps, root, 1, dead); ok {
		t.Errorf("should not return a dead pid")
	}

	// a root that is a prefix of the real one must not match
	if _, ok := parseDaemonPID(ps, "/Users/x/src/cla", 1, alive); ok {
		t.Errorf("prefix of a real root must not match (trailing-space guard)")
	}

	// no daemon at all → not found
	if _, ok := parseDaemonPID("  100 /usr/bin/bash\n", root, 1, alive); ok {
		t.Errorf("no daemon line should report not found")
	}
}
