package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
	)
	return cmd
}

func newHookInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install bough's canonical hook handlers into .claude/settings.json",
		RunE: func(c *cobra.Command, _ []string) error {
			settingsPath, err := defaultClaudeSettingsPath()
			if err != nil {
				return err
			}
			m := hooks.New(settingsPath)
			return m.Install(commandCtx(c), "bough hook handle")
		},
	}
}

func newHookUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove bough's hook handlers from .claude/settings.json",
		RunE: func(c *cobra.Command, _ []string) error {
			settingsPath, err := defaultClaudeSettingsPath()
			if err != nil {
				return err
			}
			m := hooks.New(settingsPath)
			return m.Uninstall(commandCtx(c))
		},
	}
}

func newHookListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Print every hook handler currently wired in .claude/settings.json",
		RunE: func(c *cobra.Command, _ []string) error {
			settingsPath, err := defaultClaudeSettingsPath()
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
				entries, ok := set[event]
				if !ok {
					continue
				}
				fmt.Fprintf(c.OutOrStdout(), "%s:\n", event)
				for _, e := range entries {
					fmt.Fprintf(c.OutOrStdout(), "  - %s %q (matcher=%q)\n", e.Type, e.Command, e.Matcher)
				}
			}
			return nil
		},
	}
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
session. v0.7.0 ships canonical fixtures under
internal/hooks/testdata/ that golden-test the install / handler
pair end-to-end.`,
		RunE: func(c *cobra.Command, _ []string) error {
			if event == "" {
				return fmt.Errorf("--event is required (e.g. --event PreToolUse)")
			}
			if fixture == "" {
				return fmt.Errorf("--fixture is required (= path to the JSON payload Claude Code would have sent on stdin)")
			}
			payload, err := os.ReadFile(fixture)
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
	cmd.Flags().StringVar(&event, "event", "", "hook event name (e.g. PreToolUse, PostToolUse, SessionEnd)")
	cmd.Flags().StringVar(&fixture, "fixture", "", "path to a JSON fixture file (e.g. internal/hooks/testdata/pretooluse.json)")
	return cmd
}

func newHookDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report bough's hook wiring + observer + cost posture in one place",
		Long: `bough hook doctor is the v0.7.0 transparency surface.
Round 5 review front-loaded this from v0.7.1 to v0.7.0 because
silent billing / silent observer / silent Haiku regressions are
exactly what ECC has historically struggled with and bough should
visibly avoid. The body lands in the v0.7.0 transparency sub-phase
once the observer raw-trace persistence is in.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return hooks.ErrNotYetWired
		},
	}
}

// defaultClaudeSettingsPath returns the per-project .claude/
// settings.json bough manages. v0.7.0 anchors against the CLI's
// current working directory so each monorepo gets an isolated hook
// wiring; v0.7.x adds --scope=user / --scope=project flags to
// reach the global surface explicitly.
func defaultClaudeSettingsPath() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	return filepath.Join(cwd, ".claude", "settings.json"), nil
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
