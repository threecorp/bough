package cli

import (
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
			ident, layout, err := resolveObserverProject(root)
			if err != nil {
				return err
			}
			if err := layout.EnsureProjectDirs(ident.ID); err != nil {
				return err
			}
			pidPath := observerPidFile(layout, ident.ID)
			if running, pid := daemonRunning(pidPath); running {
				return fmt.Errorf("observer already running (pid %d); `bough observer stop` first", pid)
			}
			// Re-exec ourselves in --daemon mode as a detached child.
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("observer start: locate self: %w", err)
			}
			child := makeDetachedCmd(exe, []string{
				"observer", "_run-daemon",
				"--root", ident.Root,
				"--interval", strconv.Itoa(interval),
			})
			if err := child.Start(); err != nil {
				return fmt.Errorf("observer start: spawn: %w", err)
			}
			if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
				return fmt.Errorf("observer start: write pid: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "observer started (pid %d, interval %ds)\nlog: %s\n",
				child.Process.Pid, interval, layout.ObserverLog(ident.ID))
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().IntVar(&interval, "interval", 600, "seconds between extraction passes (>= 60 recommended)")
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
			running, pid := daemonRunning(pidPath)
			if !running {
				// The pid file is stale or missing, but a daemon may
				// still be alive with a pid the file never captured (a
				// spawn race, or one started out-of-band). Discover it
				// by command line before giving up, so a live daemon is
				// never orphaned by a bad pid file.
				if dpid, ok := findDaemonByRoot(ident.Root); ok {
					running, pid = true, dpid
				}
			}
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
				_ = syscall.Kill(pid, syscall.SIGKILL)
				waitGone(pid, 2*time.Second)
				via = "SIGKILL"
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
			running, pid := daemonRunning(pidPath)
			if !running {
				if dpid, ok := findDaemonByRoot(ident.Root); ok {
					running, pid = true, dpid
				}
			}
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
			// launched out-of-band. This is the authoritative writer.
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
			for {
				appendDaemonLog(logPath, fmt.Sprintf("tick: observer run-once (interval %ds)", interval))
				// run the extraction pass in-process by invoking the
				// run-once command path. We shell out to keep the
				// limiter / provider lifecycle identical to a manual
				// run; CommandContext lets a mid-pass SIGTERM interrupt
				// the claude --print call instead of waiting it out.
				runObserverOnceQuiet(ctx, ident.Root)
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
	cmd.Flags().IntVar(&interval, "interval", 600, "seconds between passes")
	return cmd
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
	ident, err := homunculus.DetectIdentity(cwd)
	if err != nil {
		return homunculus.ProjectIdentity{}, homunculus.Layout{}, err
	}
	return ident, homunculus.NewLayout(), nil
}

// daemonRunning reads the PID file and checks whether that process is
// alive (= signal 0). A stale PID file (process gone) reads as not
// running.
func daemonRunning(pidPath string) (bool, int) {
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false, 0
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false, pid
	}
	return true, pid
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
	marker := "--root " + root + " "
	for _, line := range strings.Split(psOutput, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "observer _run-daemon") || !strings.Contains(line, marker) {
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
