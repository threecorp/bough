package cli

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// TestStartObserverDaemon_DoesNotDuplicateOrphanedDaemon is the regression
// test for the gap this retrospective review found: startObserverDaemon's
// check-then-spawn gate trusted the pid file alone (daemonRunning), while
// `observer stop` / `status` / doctor's posture check already fell back to
// a process-table scan (findDaemonByRoot) when the pid file was missing,
// stale, or never captured a spawn race — the exact fix #47 shipped for
// #45. That fallback was never carried into the shared start gate, so a
// live daemon left behind with no (or a wrong) pid file made `start` — and,
// after this PR wired it onto every UserPromptSubmit, autostart — spawn a
// SECOND daemon for the same root, silently doubling the minting rate.
//
// This spawns a real subprocess whose command line matches the daemon
// marker startObserverDaemon itself looks for (`observer _run-daemon
// --root <root> ...`), deliberately WITHOUT ever writing a pid file for
// it (the "pid file lost while the daemon stays alive" scenario), then
// asserts startObserverDaemon recognizes it instead of spawning another.
func TestStartObserverDaemon_DoesNotDuplicateOrphanedDaemon(t *testing.T) {
	t.Setenv("BOUGH_HOMUNCULUS_DIR", t.TempDir())
	root := t.TempDir()

	ident, layout, err := resolveObserverProject(root)
	if err != nil {
		t.Fatalf("resolveObserverProject: %v", err)
	}
	if err := layout.EnsureProjectDirs(ident.ID); err != nil {
		t.Fatalf("EnsureProjectDirs: %v", err)
	}
	pidPath := observerPidFile(layout, ident.ID)

	// Spawn a real "orphaned" daemon: its command line matches
	// daemonLineMatches for ident.Root, but no pid file is ever written
	// for it — reproducing a lost/never-captured pid file with the
	// process still alive.
	helper := exec.Command(os.Args[0],
		"-test.run=TestHelperObserverDaemonProcess", "--",
		"observer", "_run-daemon", "--root", ident.Root, "--interval", "600")
	helper.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer func() { _ = helper.Process.Kill() }()

	// Give findDaemonByRoot's `ps -axo pid=,command=` scan something to
	// find (process table visibility is not instantaneous under load).
	if !waitForCondition(2*time.Second, func() bool {
		_, ok := findDaemonByRoot(ident.Root)
		return ok
	}) {
		t.Fatal("helper process never became visible to findDaemonByRoot")
	}
	if _, err := os.Stat(pidPath); err == nil {
		t.Fatal("test setup bug: pid file must not exist yet")
	}

	started, pid, _, err := startObserverDaemon(root, 600)
	if err != nil {
		t.Fatalf("startObserverDaemon: %v", err)
	}
	if started {
		t.Fatalf("started = true, want false — a live orphaned daemon (pid %d) must be recognized, not duplicated", pid)
	}
	if pid != helper.Process.Pid {
		t.Errorf("pid = %d, want the orphaned helper's pid %d", pid, helper.Process.Pid)
	}

	// The pid file should now be self-healed to the discovered pid, so a
	// second call resolves it via the cheap pid-file path instead of
	// paying for another process-table scan.
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read pid file after self-heal: %v", err)
	}
	if got, _ := strconv.Atoi(string(raw)); got != helper.Process.Pid {
		t.Errorf("pid file = %q, want self-healed to %d", raw, helper.Process.Pid)
	}
}

// waitForCondition polls cond every 20ms until it reports true or timeout
// elapses, returning whether it ever became true. Process-table visibility
// for a just-started subprocess is not instantaneous on every platform.
func waitForCondition(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// TestHelperObserverDaemonProcess is a re-exec'd helper subprocess (the
// standard os/exec TestHelperProcess pattern) that just hangs until
// killed, so its own command line — not `bough`'s — is what ps sees. It
// is a no-op unless launched via GO_WANT_HELPER_PROCESS, so `go test`
// running it directly does nothing.
func TestHelperObserverDaemonProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
}
