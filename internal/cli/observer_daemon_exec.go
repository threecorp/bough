package cli

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// makeDetachedCmd builds an exec.Cmd that, when Started, becomes a
// session leader detached from the parent's controlling terminal
// (Setsid) so it survives the shell that launched it. Unix-only;
// bough's release matrix is darwin + linux.
func makeDetachedCmd(exe string, args []string) *exec.Cmd {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Detach stdio so the daemon does not hold the parent's pipes.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	// Inherit the env (already sanitised per-call inside claudecli).
	cmd.Env = os.Environ()
	return cmd
}

// runObserverOnceQuiet runs a single extraction pass by spawning
// `bough observer run-once --root <root>` as a subprocess. We spawn
// rather than call in-process so each tick gets a fresh provider /
// limiter lifecycle identical to a manual run, and a panic in one
// pass cannot kill the daemon loop. It returns the subprocess exit
// status so the caller (tickOnce) can feed the daemon-lifetime
// limiter's circuit breaker; a nil error means the pass succeeded.
func runObserverOnceQuiet(ctx context.Context, root string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// The canonical path, not the deprecated `observer run-once` alias: the
	// daemon re-execs this every tick, and pointing machinery at a deprecated
	// spelling is how a migration notice ends up firing on a timer.
	c := exec.CommandContext(ctx, exe, "instinct", "observer", "run-once", "--root", root)
	c.Stdin = nil
	c.Stdout = nil
	c.Stderr = nil
	c.Env = os.Environ()
	// Run the pass in its own process group (Setpgid) so that, on daemon
	// shutdown (ctx cancel), we kill the WHOLE group — the run-once child AND
	// the `claude --print` grandchild it spawned. CommandContext's default
	// cancel kills only the direct child, leaving the grandchild (and its
	// in-flight subscription call) orphaned to init after `observer stop`.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return os.ErrProcessDone
		}
		// Negative pid → signal the process group led by the child.
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
	// Return the exit status so tickOnce can record success/failure on
	// the daemon-lifetime limiter (its circuit breaker). A failed pass
	// also logs via the run-once path; the daemon loop continues either
	// way.
	return c.Run()
}
