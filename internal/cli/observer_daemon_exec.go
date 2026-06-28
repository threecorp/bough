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
// pass cannot kill the daemon loop.
func runObserverOnceQuiet(ctx context.Context, root string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	c := exec.CommandContext(ctx, exe, "observer", "run-once", "--root", root)
	c.Stdin = nil
	c.Stdout = nil
	c.Stderr = nil
	c.Env = os.Environ()
	// Best-effort: a failed pass logs via the run-once path; the
	// daemon loop continues regardless.
	_ = c.Run()
}
