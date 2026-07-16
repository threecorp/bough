package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/provider/claudecli"
)

// The observer daemon is an opt-in background process that runs
// `bough observer run-once` on an interval. v0.9.0 shipped run-once
// (= the synchronous extraction pass a hook fires); v0.9.2 adds the
// daemon for operators who want continuous extraction without wiring
// SessionEnd. Default is disabled — the operator starts it explicitly.
//
// State lives under <homunculus>/projects/<id>/ so the daemon is
// per-project + survives across shells:
//
//	observer.pid   = the daemon's PID
//	observer.log   = append-only run log (= already the run-once log)
//
// We deliberately do NOT use systemd / launchd — that would bind the
// daemon to a single OS's service manager and complicate cleanup. A
// PID file + signal is portable across macOS / Linux.

func observerPidFile(layout homunculus.Layout, projectID string) string {
	return filepath.Join(layout.ProjectDir(projectID), "observer.pid")
}

// defaultObserverIntervalSec is the daemon's default minting cadence
// when no --interval / instinct.observer.interval_sec is given. Named so
// the CLI flag defaults and the autostart fallback (observerAutostartInterval
// in hook.go) share one source of truth instead of three independent
// bare-600 literals.
const defaultObserverIntervalSec = 600

// startLockStaleAfter bounds how long a start-lock file (see
// acquireStartLock) is honored before a later caller reclaims it as
// abandoned. The critical section it guards is a liveness check plus one
// process spawn — well under a second normally — so a lock still held
// after this long means its owner crashed mid-section without reaching
// the deferred release, not that it is legitimately still working.
const startLockStaleAfter = 10 * time.Second

// acquireStartLock atomically claims lockPath as a short-lived mutex
// around startObserverDaemon's check-then-spawn section, so two
// concurrent callers for the same root cannot both observe "not
// running" and each spawn their own daemon. A lock older than
// startLockStaleAfter is treated as abandoned (its holder crashed before
// releasing) and reclaimed once. ok=false means another live caller
// currently holds it; the caller should not spawn.
func acquireStartLock(lockPath string) (release func(), ok bool) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err == nil {
		f.Close()
		return func() { _ = os.Remove(lockPath) }, true
	}
	if !os.IsExist(err) {
		return func() {}, false
	}
	if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > startLockStaleAfter {
		if os.Remove(lockPath) == nil {
			f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err == nil {
				f.Close()
				return func() { _ = os.Remove(lockPath) }, true
			}
		}
	}
	return func() {}, false
}

// startObserverDaemon ensures a detached observer daemon is running for
// the monorepo resolved from root ("" = cwd), starting one only if none
// is. started=false with a non-error means one was already running (pid =
// its pid), OR a concurrent caller currently owns the start decision (see
// below) — either way the caller must not spawn a second daemon. Shared
// by `bough observer start` and the UserPromptSubmit autostart gate so
// the spawn + pid-file bookkeeping cannot diverge between the two entry
// points.
func startObserverDaemon(root string, interval int) (started bool, pid int, logPath string, err error) {
	ident, layout, err := resolveObserverProject(root)
	if err != nil {
		return false, 0, "", err
	}
	if err := layout.EnsureProjectDirs(ident.ID); err != nil {
		return false, 0, "", err
	}
	logPath = layout.ObserverLog(ident.ID)
	pidPath := observerPidFile(layout, ident.ID)

	// resolveObserverProject canonicalises every worktree of a monorepo to
	// the SAME root/pid path, and the autostart gate now calls this on
	// every UserPromptSubmit from a fresh `bough hook handle` process —
	// so two sessions in different worktrees of the same monorepo can
	// race here. Without a lock, both could observe "not running" before
	// either has written the pid file and each spawn their own orphaned
	// daemon, silently doubling the LLM call rate. The lock only needs to
	// live for this function's check-then-spawn section.
	release, ok := acquireStartLock(pidPath + ".lock")
	if !ok {
		if running, existing := findLiveDaemon(pidPath, ident.Root); running {
			return false, existing, logPath, nil
		}
		return false, 0, logPath, nil
	}
	defer release()

	// findLiveDaemon (not the bare pid-file check) so a pid file that is
	// missing, stale, or was never written — a daemon started out-of-band,
	// one whose spawn raced the pid-file write, or one left behind after
	// an operator cleared the homunculus dir by hand — still counts as
	// "already running". Before this fell back only in `stop` / `status`
	// / doctor's posture check; `start`'s own spawn gate trusted the pid
	// file alone, so exactly that situation made it spawn a SECOND daemon
	// for the same root. That gap was tolerable while `start` was a rare,
	// deliberate operator command; autostart now runs this same gate on
	// every UserPromptSubmit, which turns a rare edge case into a
	// standing risk of silently doubling the minting rate.
	if running, existing := findLiveDaemon(pidPath, ident.Root); running {
		return false, existing, logPath, nil
	}
	// Re-exec ourselves in --daemon mode as a detached child.
	exe, err := os.Executable()
	if err != nil {
		return false, 0, logPath, fmt.Errorf("observer start: locate self: %w", err)
	}
	child := makeDetachedCmd(exe, []string{
		"observer", "_run-daemon",
		"--root", ident.Root,
		"--interval", strconv.Itoa(interval),
	})
	if err := child.Start(); err != nil {
		return false, 0, logPath, fmt.Errorf("observer start: spawn: %w", err)
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
		return false, child.Process.Pid, logPath, fmt.Errorf("observer start: write pid: %w", err)
	}
	return true, child.Process.Pid, logPath, nil
}

// findLiveDaemon reports whether a live observer daemon exists for root,
// checking the pid file first (the cheap, common-case path) and falling
// back to a process-table scan when the file is missing, stale, or names
// a pid that is not our daemon. Every caller that must decide "is a
// daemon already up for this root" — the start gate, `stop`, `status`,
// and doctor's posture line — goes through this one function so the
// answer cannot diverge between "should I spawn a new one" and "is one
// already running" the way it used to (the start gate trusted the pid
// file alone and could spawn a duplicate next to a daemon the other
// three call sites would have found just fine).
//
// When the fallback scan is what found it, the pid file is missing or
// wrong by definition, so it is best-effort self-healed here to the
// discovered pid — the same "own the pid file" idea `_run-daemon`
// already applies to itself on startup — so the next lookup for this
// root is the cheap pid-file path again instead of paying for another
// full process-table scan.
func findLiveDaemon(pidPath, root string) (running bool, pid int) {
	if running, pid := daemonRunning(pidPath, root); running {
		return true, pid
	}
	if dpid, ok := findDaemonByRoot(root); ok {
		_ = os.WriteFile(pidPath, []byte(strconv.Itoa(dpid)), 0o644)
		return true, dpid
	}
	return false, 0
}

// observerDaemonRunning reports whether a live observer daemon exists for
// the monorepo resolved from root. Used by `bough doctor` to show the
// autostart posture. Best-effort: a resolution failure reads as "not
// running" rather than erroring the doctor report.
func observerDaemonRunning(root string) bool {
	ident, layout, err := resolveObserverProject(root)
	if err != nil {
		return false
	}
	running, _ := findLiveDaemon(observerPidFile(layout, ident.ID), ident.Root)
	return running
}

// newObserverStartCmd / Stop / Status extend the `bough observer`
// namespace. They are added to newObserverCmd in observer.go.
func newObserverStartCmd() *cobra.Command {
	var (
		root     string
		interval int
	)
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the background observer daemon (opt-in; runs observer run-once on an interval)",
		Long: `bough observer start launches a background process that runs
the extraction pass every --interval seconds. It is opt-in — the
operator starts it explicitly; nothing auto-starts it. The PID lives
under the project's homunculus dir so a later "bough observer stop"
finds it across shells.

Each tick spawns the same claude --print pass "bough observer
run-once" runs, so the v0.9.0 self-DoS limiter still caps the call
rate.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			started, pid, logPath, err := startObserverDaemon(root, interval)
			if err != nil {
				return err
			}
			if !started {
				return fmt.Errorf("observer already running (pid %d); `bough observer stop` first", pid)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "observer started (pid %d, interval %ds)\nlog: %s\n", pid, interval, logPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().IntVar(&interval, "interval", defaultObserverIntervalSec, "seconds between extraction passes (>= 60 recommended)")
	return cmd
}

func newObserverStopCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the background observer daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ident, layout, err := resolveObserverProject(root)
			if err != nil {
				return err
			}
			pidPath := observerPidFile(layout, ident.ID)
			// findLiveDaemon: the pid file may be stale or missing while a
			// daemon is still alive with a pid the file never captured (a
			// spawn race, or one started out-of-band) — the fallback
			// process-table scan finds it before giving up, so a live
			// daemon is never orphaned by a bad pid file.
			running, pid := findLiveDaemon(pidPath, ident.Root)
			if !running {
				fmt.Fprintln(cmd.OutOrStdout(), "observer not running")
				_ = os.Remove(pidPath)
				return nil
			}
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				return fmt.Errorf("observer stop: signal pid %d: %w", pid, err)
			}
			// Escalate to SIGKILL if the daemon does not exit promptly.
			// The daemon now shuts down on context-cancel (v0.9.9), but
			// a pass wedged in a long claude --print call — or any future
			// hang — must not leave a daemon ticking after stop reports
			// success.
			via := "SIGTERM"
			if !waitGone(pid, 3*time.Second) {
				// Re-verify identity before escalating: after SIGTERM the
				// daemon may have exited and the OS recycled its pid to an
				// unrelated process within the grace window. SIGKILLing a bare
				// pid would then kill that innocent process — the recycled-pid
				// class closed for the SIGTERM target in v0.9.17, closed here
				// for the escalation path too. If the pid is gone or no longer
				// our daemon, SIGTERM worked (or it was recycled) — don't kill.
				if pidIsObserverDaemon(pid, ident.Root) {
					_ = syscall.Kill(pid, syscall.SIGKILL)
					if waitGone(pid, 2*time.Second) {
						via = "SIGKILL"
					} else {
						via = "SIGKILL (still present)"
					}
				}
			}
			_ = os.Remove(pidPath)
			fmt.Fprintf(cmd.OutOrStdout(), "observer stopped (pid %d, via %s)\n", pid, via)
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	return cmd
}

func newObserverStatusCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report whether the observer daemon is running",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ident, layout, err := resolveObserverProject(root)
			if err != nil {
				return err
			}
			pidPath := observerPidFile(layout, ident.ID)
			running, pid := findLiveDaemon(pidPath, ident.Root)
			if running {
				fmt.Fprintf(cmd.OutOrStdout(), "observer running (pid %d)\nlog: %s\n", pid, layout.ObserverLog(ident.ID))
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "observer not running")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	return cmd
}

// newObserverRunDaemonCmd is the hidden inner loop the detached child
// runs. It sleeps --interval seconds, runs one extraction pass, and
// repeats until SIGTERM. Not for direct operator use.
func newObserverRunDaemonCmd() *cobra.Command {
	var (
		root     string
		interval int
	)
	cmd := &cobra.Command{
		Use:    "_run-daemon",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ident, layout, err := resolveObserverProject(root)
			if err != nil {
				return err
			}
			// Own the pid file: record our REAL pid so `observer stop`
			// / `status` always target the live daemon even if `start`
			// captured a different pid (a spawn race) or the daemon was
			// launched out-of-band. This is the authoritative writer; ensure
			// the project dir exists first so an out-of-band launch (where
			// start never ran EnsureProjectDirs) still leaves a pid file.
			_ = layout.EnsureProjectDirs(ident.ID)
			_ = os.WriteFile(observerPidFile(layout, ident.ID), []byte(strconv.Itoa(os.Getpid())), 0o644)
			logPath := layout.ObserverLog(ident.ID)
			if interval < 60 {
				interval = 60
			}
			// The root command installs signal.NotifyContext for
			// SIGTERM/SIGINT (cmd/bough/main.go), so those signals
			// CANCEL this context instead of terminating the process by
			// default. The loop therefore MUST watch ctx.Done() — before
			// v0.9.9 it looped on a bare time.Sleep and ignored the
			// cancelled context, so the daemon survived SIGTERM and only
			// SIGKILL could stop it.
			ctx := commandCtx(cmd)
			// tickLimiter lives for the daemon's whole lifetime (unlike
			// runObserverOnceQuiet's subprocess, which deliberately gets
			// a fresh, unpersisted claudecli.Limiter every tick for
			// crash isolation — see its doc comment). This is the one
			// instance that actually enforces the advertised N/hour
			// self-DoS ceiling across ticks. MaxCallsPerSession is
			// disabled (0): "session" there means "one manual run-once
			// invocation", not "daemon lifetime", so the per-session cap
			// does not apply here.
			tickLimiter := claudecli.NewLimiter()
			tickLimiter.MaxCallsPerSession = 0
			for {
				tickOnce(ctx, logPath, interval, ident.Root, tickLimiter, runObserverOnceQuiet)
				select {
				case <-ctx.Done():
					appendDaemonLog(logPath, "shutdown: context cancelled, daemon exiting")
					_ = os.Remove(observerPidFile(layout, ident.ID))
					return nil
				case <-time.After(time.Duration(interval) * time.Second):
				}
			}
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root")
	cmd.Flags().IntVar(&interval, "interval", defaultObserverIntervalSec, "seconds between passes")
	return cmd
}

// tickOnce runs (or skips) a single daemon tick against the shared,
// daemon-lifetime limiter. Extracted from the daemon loop so the
// hourly-cap accumulation can be tested by calling it directly,
// without waiting on real --interval sleeps. When the limiter's
// hourly cap (or circuit breaker) rejects the tick, it is logged and
// skipped rather than firing runTick.
func tickOnce(ctx context.Context, logPath string, interval int, root string, limiter *claudecli.Limiter, runTick func(context.Context, string) error) {
	if err := limiter.Acquire(); err != nil {
		appendDaemonLog(logPath, fmt.Sprintf("tick skipped: %v", err))
		return
	}
	appendDaemonLog(logPath, fmt.Sprintf("tick: observer run-once (interval %ds)", interval))
	// run the extraction pass in-process by invoking the run-once
	// command path. We shell out to keep the limiter / provider
	// lifecycle identical to a manual run; CommandContext lets a
	// mid-pass SIGTERM interrupt the claude --print call instead of
	// waiting it out.
	err := runTick(ctx, root)
	// Feed the pass result back to the daemon-lifetime limiter so its
	// circuit breaker can trip after CircuitBreakerN consecutive
	// failures — without this the breaker never sees a failure and never
	// opens. Skip recording entirely when the daemon is shutting down
	// (ctx cancelled): the pass was SIGKILLed by our own Cancel, not a
	// real transient failure, and counting it would trip the breaker on
	// a clean stop.
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		limiter.RecordFailure()
		appendDaemonLog(logPath, fmt.Sprintf("tick failed: %v", err))
	} else {
		limiter.RecordSuccess()
	}
}

func resolveObserverProject(root string) (homunculus.ProjectIdentity, homunculus.Layout, error) {
	cwd := root
	if cwd == "" {
		w, err := os.Getwd()
		if err != nil {
			return homunculus.ProjectIdentity{}, homunculus.Layout{}, err
		}
		cwd = w
	}
	// Canonicalise to the monorepo root so start (from the repo root) and
	// stop (possibly from a sub-dir / worktree) resolve the SAME root —
	// otherwise the daemon records `--root <repoRoot>` while stop's
	// findDaemonByRoot looks for `--root <subdir>` and orphans it.
	cwd = resolveMonorepoRoot(cwd)
	ident, err := homunculus.DetectIdentity(cwd)
	if err != nil {
		return homunculus.ProjectIdentity{}, homunculus.Layout{}, err
	}
	return ident, homunculus.NewLayout(), nil
}

// daemonRunning reads the PID file and reports whether that pid is a LIVE
// observer daemon for this root. Signal-0 liveness alone is not enough: a
// crash/SIGKILL leaves a stale pid file, and if the OS has recycled that
// pid, signalling it would hit an unrelated process — so `stop` must not
// trust the file's pid without verifying it is actually our daemon. The
// command-line check closes that recycled-pid kill. A pid that exists but
// is not our daemon reads as not running (and `stop` then falls through to
// findDaemonByRoot, which verifies the same way).
func daemonRunning(pidPath, root string) (bool, int) {
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false, 0
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false, pid // process gone
	}
	if !pidIsObserverDaemon(pid, root) {
		return false, pid // pid exists but is not our daemon (stale file / recycled pid)
	}
	return true, pid
}

// pidIsObserverDaemon reports whether pid's command line is an
// `observer _run-daemon --root <root>` — the identity gate that keeps
// daemonRunning from trusting a recycled pid.
func pidIsObserverDaemon(pid int, root string) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	return daemonLineMatches(strings.TrimSpace(string(out)), root)
}

// daemonLineMatches is the pure marker check shared by parseDaemonPID and
// pidIsObserverDaemon: the line is an observer daemon bound to root. The
// root is matched as `--root <root> ` (trailing space) so a path that is a
// prefix of another cannot match, and `--interval` always follows --root
// so the trailing space is present.
func daemonLineMatches(line, root string) bool {
	return strings.Contains(line, "observer _run-daemon") && strings.Contains(line, "--root "+root+" ")
}

// waitGone polls until pid is no longer signalable (= the process is
// gone) or timeout elapses, returning true once it is gone. stop uses
// it to confirm SIGTERM actually took effect before reporting success,
// and to gate the SIGKILL escalation.
func waitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return syscall.Kill(pid, 0) != nil
}

// findDaemonByRoot discovers a live `observer _run-daemon --root <root>`
// process by scanning the process table. It is the fallback path when
// the pid file is stale or missing — a daemon started out-of-band, or
// one whose `start` recorded the wrong pid, must still be stoppable so
// it cannot be orphaned and keep ticking against a project the operator
// believes is idle.
func findDaemonByRoot(root string) (int, bool) {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return 0, false
	}
	return parseDaemonPID(string(out), root, os.Getpid(), func(pid int) bool {
		return syscall.Kill(pid, 0) == nil
	})
}

// parseDaemonPID is the pure, testable core of findDaemonByRoot. Given
// `ps -axo pid=,command=` output it returns the pid of the first live
// `observer _run-daemon` line bound to root, skipping self. The root is
// matched as `--root <root> ` (trailing space) so a path that is a
// prefix of another root cannot match by mistake (the daemon always has
// ` --interval` after --root, so a trailing space is always present).
func parseDaemonPID(psOutput, root string, self int, alive func(int) bool) (int, bool) {
	for _, line := range strings.Split(psOutput, "\n") {
		line = strings.TrimSpace(line)
		if !daemonLineMatches(line, root) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 || pid == self {
			continue
		}
		if alive(pid) {
			return pid, true
		}
	}
	return 0, false
}

func appendDaemonLog(path, msg string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[daemon] %s\n", msg)
}
