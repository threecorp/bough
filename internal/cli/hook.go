package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/hooks"
)

// newHookCmd wires `bough hook install / uninstall / list / replay
// / doctor`. The v0.7.0 Bootstrap safety floor plan calls for hook
// auto-wire to ship alongside a replay harness on day one (= round
// 5 review insistence), so the cobra surface lands in the first
// v0.7.0 commit even though most subcommands return
// hooks.ErrNotYetWired until the body work catches up. Surfacing
// the CLI shape early lets fixture data, docs, and integration
// scripts develop in parallel rather than block on each other.
func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Manage Claude Code hook handlers bough writes into .claude/settings.json",
		Long: `bough hook manages the handlers an operator wires into
Claude Code's .claude/settings.json so bough's observer / bootstrap
loop fires on session lifecycle events.

The subcommands keep the JSON round-trip safe — hand-edited entries
the operator added by mouse stay put; only bough's canonical
entries get reconciled.

v0.7.0 first commit lands the cobra surface plus the
internal/hooks/ package skeleton. The Manager bodies (install /
uninstall / list / replay / doctor) wire in across the rest of the
v0.7.0 sprint per docs/ROADMAP.md.`,
	}
	cmd.AddCommand(
		newHookInstallCmd(),
		newHookUninstallCmd(),
		newHookListCmd(),
		newHookReplayCmd(),
		newHookDoctorCmd(),
		newHookHandleCmd(),
	)
	return cmd
}

func newHookInstallCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install bough's canonical hook handlers into .claude/settings.json",
		RunE: func(c *cobra.Command, _ []string) error {
			settingsPath, err := claudeSettingsPath(HookScope(scope))
			if err != nil {
				return err
			}
			m := hooks.New(settingsPath)
			return m.Install(commandCtx(c), "bough hook handle")
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "project", "settings.json scope: project (= cwd/.claude) | user (= ~/.claude)")
	return cmd
}

func newHookUninstallCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove bough's hook handlers from .claude/settings.json",
		RunE: func(c *cobra.Command, _ []string) error {
			settingsPath, err := claudeSettingsPath(HookScope(scope))
			if err != nil {
				return err
			}
			m := hooks.New(settingsPath)
			return m.Uninstall(commandCtx(c))
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "project", "settings.json scope: project | user")
	return cmd
}

func newHookListCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print every hook handler currently wired in .claude/settings.json",
		RunE: func(c *cobra.Command, _ []string) error {
			settingsPath, err := claudeSettingsPath(HookScope(scope))
			if err != nil {
				return err
			}
			m := hooks.New(settingsPath)
			set, err := m.List(commandCtx(c))
			if err != nil {
				return err
			}
			if len(set) == 0 {
				fmt.Fprintf(c.OutOrStdout(), "(no hooks wired in %s)\n", settingsPath)
				return nil
			}
			for _, event := range hooks.AllEvents() {
				groups, ok := set[event]
				if !ok {
					continue
				}
				fmt.Fprintf(c.OutOrStdout(), "%s:\n", event)
				for _, g := range groups {
					matcher := g.Matcher
					if matcher == "" {
						matcher = "*"
					}
					for _, e := range g.Hooks {
						fmt.Fprintf(c.OutOrStdout(), "  - matcher=%s %s %q\n", matcher, e.Type, e.Command)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "project", "settings.json scope: project | user")
	return cmd
}

func newHookReplayCmd() *cobra.Command {
	var (
		event   string
		fixture string
	)
	cmd := &cobra.Command{
		Use:   "replay",
		Short: "Replay a fixture JSON payload through the bough hook handler for debugging",
		Long: `bough hook replay drives a recorded hook-event payload
through the bough handler so an operator can sanity-check the
wiring against a fixture file without touching a live Claude Code
session. Canonical fixtures for both wired events live under
internal/hooks/testdata/ and golden-test the install / handler
pair end-to-end.`,
		RunE: func(c *cobra.Command, _ []string) error {
			if event == "" {
				return fmt.Errorf("--event is required (e.g. --event WorktreeCreate)")
			}
			if fixture == "" {
				return fmt.Errorf("--fixture is required (= '-' for stdin, or path to a JSON payload file)")
			}
			var payload []byte
			var err error
			if fixture == "-" {
				payload, err = io.ReadAll(c.InOrStdin())
			} else {
				payload, err = os.ReadFile(fixture)
			}
			if err != nil {
				return fmt.Errorf("read fixture %s: %w", fixture, err)
			}
			settingsPath, err := defaultClaudeSettingsPath()
			if err != nil {
				return err
			}
			m := hooks.New(settingsPath)
			result, err := m.Replay(commandCtx(c), hooks.HookEvent(event), payload)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(),
				"event=%s exitCode=%d\nstdout: %s\nstderr: %s\n",
				result.Event, result.ExitCode, result.Stdout, result.Stderr)
			return nil
		},
	}
	cmd.Flags().StringVar(&event, "event", "", "hook event name (WorktreeCreate | WorktreeRemove)")
	cmd.Flags().StringVar(&fixture, "fixture", "", "path to a JSON fixture file (or '-' to read from stdin)")
	return cmd
}

func newHookDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report bough's hook wiring and engine-plugin posture in one place",
		Long: `bough hook doctor is the transparency surface: which events are
wired and by whom, whether any stale wiring from a retired event is
still in settings.json, whether the worktree containers a host would
refuse exist, and whether the engine plugins are reachable. Same body
as the top-level "bough doctor" alias.`,
		RunE: func(c *cobra.Command, _ []string) error {
			return runDoctor(c)
		},
	}
}

// runDoctor is the shared body between `bough doctor` (= top-level
// alias) and `bough hook doctor`. Both surfaces print the same
// report so operators do not have to remember which spelling to
// use; the top-level alias matches the round 5 reviewer ask of
// having the transparency check reachable without remembering the
// `hook` namespace.
func runDoctor(c *cobra.Command) error {
	settingsPath, err := defaultClaudeSettingsPath()
	if err != nil {
		return err
	}
	m := hooks.New(settingsPath)
	report, err := m.Doctor(commandCtx(c))
	if err != nil {
		return err
	}
	w := c.OutOrStdout()
	report.Render(w)
	renderWorktreeIsolation(commandCtx(c), w)
	renderEnginePlugins(commandCtx(c), w)
	renderRetiredConfig(c, w)
	return nil
}

// newHookHandleCmd wires `bough hook handle`, the dispatcher Claude
// Code invokes for every wired hook entry: the event name on the
// --event flag, the JSON payload on stdin.
//
// Hidden from the human surface because Claude Code is the only
// expected caller — wrapping it in a `bough hook` namespace lets
// `bough hook replay` reuse the same payload format for golden
// tests without colliding with operator workflows.
//
// Since v0.27.0 the only events with a body are WorktreeCreate and
// WorktreeRemove. The six events the continuous-learning loop used to
// drive are accepted and ignored (see hooks.RetiredEvents) so wiring
// left in an operator's settings.json — or cached inside an
// already-installed bough-hooks plugin — keeps exiting 0 until they
// re-run `bough claude hook install`. An event that is neither wired
// nor retired is an error: a typo'd --event used to exit 0 with empty
// stdout, which a host reports as "hook succeeded but returned no
// worktree path" with nothing naming the cause.
func newHookHandleCmd() *cobra.Command {
	var event string
	cmd := &cobra.Command{
		Use:    "handle",
		Hidden: true,
		Short:  "Receive a Claude Code hook event payload via stdin and run the matching worktree action",
		RunE: func(c *cobra.Command, _ []string) error {
			if event == "" {
				return fmt.Errorf("--event is required (= called by Claude Code's settings.json wiring; see `bough claude hook install`)")
			}
			// Answered before reading stdin or touching the config: a
			// stale plugin sends six of these per tool call, and they must
			// cost nothing. stderr, never stdout — UserPromptSubmit stdout
			// is folded into the model's next turn, so a notice written
			// there would be read as context every single turn.
			if hooks.IsRetired(event) {
				fmt.Fprintf(c.ErrOrStderr(),
					"[bough] hook event %s is retired since v0.27.0 and does nothing; "+
						"run `bough claude hook install` to prune the stale wiring\n", event)
				return nil
			}
			if !hooks.IsWired(event) {
				return fmt.Errorf("unknown hook event %q (wired: %s)", event, hooks.WiredEventNames())
			}
			payload, err := io.ReadAll(c.InOrStdin())
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			// Validate the payload is JSON so a malformed Claude Code
			// event surfaces as a hook failure instead of being acted on
			// half-decoded. The raw bytes are held through so the
			// dispatchers can read fields bough does not yet model.
			if len(payload) > 0 {
				var probe map[string]any
				if err := json.Unmarshal(payload, &probe); err != nil {
					return fmt.Errorf("payload is not valid JSON: %w", err)
				}
			}
			switch event {
			case string(hooks.EventWorktreeCreate):
				// The wiring `bough claude hook install` writes routes
				// WorktreeCreate here; run the create pipeline and emit the
				// worktree path to stdout (the hook contract Claude Code
				// reads to cd into the new tree). Returning the error makes a
				// create failure surface as a hook failure.
				return dispatchWorktreeCreate(c, payload)
			case string(hooks.EventWorktreeRemove):
				return dispatchWorktreeRemove(c, payload)
			}
			// Unreachable while IsWired and this switch agree; a new event
			// added to one and not the other lands here rather than exiting
			// 0 with nothing done.
			return fmt.Errorf("hook event %q is wired but has no handler", event)
		},
	}
	cmd.Flags().StringVar(&event, "event", "", "Claude Code hook event name (e.g. WorktreeCreate)")
	return cmd
}

// resolveMonorepoRoot answers "which directory is the monorepo root
// for this cwd?" — the anchor every worktree verb keys on. A session
// inside a worktree resolves to the monorepo parent (the path before
// the worktrees/ segment); otherwise it walks up to the nearest
// ancestor holding the monorepo marker (.bough.yaml); else it falls
// back to cwd.
func resolveMonorepoRoot(cwd string) string {
	// Prefer the prefix before the worktrees segment — but only when it
	// actually holds the .bough.yaml marker, so a path that legitimately
	// contains such a segment that is not bough's does not resolve to a
	// bogus root, and a worktree sub-repo carrying a stray marker cannot
	// shadow the real root. Both the v0.11 worktrees/ and the pre-v0.11
	// hidden .worktrees/ layouts are recognised. Otherwise fall through to
	// the ancestor walk (which also finds a real worktree's monorepo root,
	// since it holds the marker).
	for _, seg := range []string{"/" + worktreesName + "/", "/" + legacyWtName + "/"} {
		if i := strings.Index(cwd, seg); i >= 0 {
			if cand := cwd[:i]; hasMonorepoMarker(cand) {
				return cand
			}
		}
	}
	dir := cwd
	for {
		if hasMonorepoMarker(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}

// hasMonorepoMarker reports whether dir holds the .bough.yaml monorepo marker.
func hasMonorepoMarker(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".bough.yaml"))
	return err == nil
}

type HookScope string

const (
	HookScopeProject HookScope = "project" // = <cwd>/.claude/settings.json (v0.7.0 default)
	HookScopeUser    HookScope = "user"    // = ~/.claude/settings.json (v0.8 addition)
)

// defaultClaudeSettingsPath returns the per-project .claude/
// settings.json bough manages.
func defaultClaudeSettingsPath() (string, error) {
	return claudeSettingsPath(HookScopeProject)
}

// claudeDir resolves the .claude directory bough manages for the requested
// scope. Project scope anchors against the current working directory; user
// scope expands ~/.claude. Every artifact kind bough installs lives under this
// one directory (settings.json for hooks, skills/ and commands/ for the rest),
// so the scope vocabulary is resolved here once rather than per kind.
func claudeDir(scope HookScope) (string, error) {
	switch scope {
	case "", HookScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getwd: %w", err)
		}
		return filepath.Join(cwd, ".claude"), nil
	case HookScopeUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("UserHomeDir: %w", err)
		}
		return filepath.Join(home, ".claude"), nil
	default:
		return "", fmt.Errorf("unknown scope %q (use 'project' or 'user')", scope)
	}
}

// claudeSettingsPath resolves the settings.json bough manages for
// the requested scope. Project scope anchors against the current
// working directory; user scope expands ~/.claude/settings.json.
func claudeSettingsPath(scope HookScope) (string, error) {
	dir, err := claudeDir(scope)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// commandCtx returns the cobra command's context or background
// when the host did not propagate one. cobra >= v1.7 always sets
// the context, but the fallback keeps the surface safe across
// shim invocations the test harness might run in.
func commandCtx(c *cobra.Command) context.Context {
	if c == nil {
		return context.Background()
	}
	ctx := c.Context()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
