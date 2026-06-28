package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/hooks"
	"github.com/ikeikeikeike/bough/internal/inject"
	"github.com/ikeikeikeike/bough/internal/qualitygate"
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
session. v0.7.0 ships canonical fixtures under
internal/hooks/testdata/ that golden-test the install / handler
pair end-to-end.`,
		RunE: func(c *cobra.Command, _ []string) error {
			if event == "" {
				return fmt.Errorf("--event is required (e.g. --event PreToolUse)")
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
	cmd.Flags().StringVar(&event, "event", "", "hook event name (e.g. PreToolUse, PostToolUse, SessionEnd)")
	cmd.Flags().StringVar(&fixture, "fixture", "", "path to a JSON fixture file (or '-' to read from stdin)")
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
visibly avoid. Same body as the top-level "bough doctor" alias.`,
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
	renderContinuousLearningPosture(w)
	return nil
}

// newHookHandleCmd wires `bough hook handle`, the v0.7.0 O-1.6
// raw-event capture dispatcher. Claude Code invokes this command
// (one per registered hook entry, per the install layout) with
// the event name on the --event flag and the JSON payload on
// stdin; the dispatcher appends one JSONL record to
// `.bough/observations.jsonl` and exits cleanly.
//
// Hidden from the human surface because Claude Code is the only
// expected caller — wrapping it in a `bough hook` namespace lets
// `bough hook replay` reuse the same payload format for golden
// tests without colliding with operator workflows.
//
// The dispatcher intentionally does no parsing of the payload
// beyond decoding it once to validate the bytes are valid JSON;
// the observer + Bootstrap Agent (= v0.7.1) own the semantic
// analysis of what each event means. Keeping the dispatcher
// dumb means a Claude Code spec drift adds a new field without
// breaking the bough side until the analysis layer is ready to
// consume it.
func newHookHandleCmd() *cobra.Command {
	var (
		event   string
		outPath string
	)
	cmd := &cobra.Command{
		Use:    "handle",
		Hidden: true,
		Short:  "Receive a Claude Code hook event payload via stdin and append to .bough/observations.jsonl",
		RunE: func(c *cobra.Command, _ []string) error {
			if event == "" {
				return fmt.Errorf("--event is required (= called by Claude Code's settings.json wiring; see `bough hook install`)")
			}
			payload, err := io.ReadAll(c.InOrStdin())
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			// Validate the payload is JSON so a malformed Claude
			// Code event surfaces as a hook failure instead of
			// silently appending garbage to the log. We hold the
			// raw bytes through so downstream tooling can decode
			// fields bough does not yet know about.
			if len(payload) > 0 {
				var probe map[string]any
				if err := json.Unmarshal(payload, &probe); err != nil {
					return fmt.Errorf("payload is not valid JSON: %w", err)
				}
			}
			// ECC model (v0.9.10): observations live in the central
			// homunculus (~/.local/share/bough-homunculus/projects/<id>/),
			// NEVER in the repo working tree, and every sub-repo / worktree
			// session pools into the one monorepo project — mirroring
			// threecorp's observe-wrapper.sh, which rewrites the hook cwd to
			// the monorepo root. An explicit --out still wins (replay /
			// conformance). Capture is best-effort: a write failure must
			// never fail the operator's tool call.
			if outPath == "" {
				outPath = resolveHomunculusObsPath()
			}
			if outPath != "" {
				rotateIfLarge(outPath)
				record := struct {
					TS      string          `json:"ts"`
					Event   string          `json:"event"`
					Payload json.RawMessage `json:"payload"`
				}{
					TS:      time.Now().UTC().Format(time.RFC3339Nano),
					Event:   event,
					Payload: json.RawMessage(payload),
				}
				if len(record.Payload) == 0 {
					record.Payload = json.RawMessage(`null`)
				}
				if line, merr := json.Marshal(record); merr == nil {
					if mkerr := os.MkdirAll(filepath.Dir(outPath), 0o755); mkerr == nil {
						if f, oerr := os.OpenFile(outPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); oerr == nil {
							_, _ = f.Write(append(line, '\n'))
							_ = f.Close()
						}
					}
				}
			}
			// v0.7.2 wires quality-gate dispatch onto the
			// observation path: if .bough.yaml declares any gates
			// that match (event, tool, file_path, repo) we run them
			// here and surface pass/fail to stderr so Claude Code's
			// next turn can see it. The runner ships its own
			// per-gate TimeoutSeconds cap (= default 60s) so a
			// hanging gate cannot block the hook.
			dispatchQualityGates(c, event, payload)

			// v0.9.2: UserPromptSubmit also injects the confidence-
			// ranked instinct block to stdout so Claude Code folds
			// it into the next turn's context. The single
			// `bough hook handle --event UserPromptSubmit` wiring
			// therefore both records the observation AND injects —
			// no separate hook entry needed. Pure filesystem; no
			// claude --print call on the prompt-submit hot path.
			// v0.9.11: the single `hook handle` wiring fans out to the
			// per-event ECC actions internally (rather than wiring N
			// separate scripts in settings.json): UserPromptSubmit
			// injects the instinct block, SessionEnd evaluates instinct
			// confidence, PreCompact preserves the top instincts to
			// stdout + MEMORY.md. All pure filesystem; LLM extraction
			// stays opt-in via the observer daemon.
			switch event {
			case string(hooks.EventUserPromptSubmit):
				dispatchInjectContext(c)
			case string(hooks.EventSessionEnd):
				_ = runSessionEnd(c.OutOrStdout(), "", "", sessionEndDefaultWindow)
			case string(hooks.EventPreCompact):
				_ = runPreserveInstincts(c.OutOrStdout(), "")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&event, "event", "", "Claude Code hook event name (e.g. PreToolUse)")
	cmd.Flags().StringVar(&outPath, "out", "", "observation log path (default: .bough/observations.jsonl)")
	return cmd
}

// resolveHomunculusObsPath returns the central homunculus observations
// file for the current monorepo project, or "" if no project identity
// can be resolved — in which case capture is silently skipped rather
// than polluting the working tree. This is the ECC model: observations
// never touch the repo working tree (cf. observe.sh writing to
// PROJECT_DIR under ~/.local/share, never the repo).
func resolveHomunculusObsPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	ident, err := homunculus.DetectIdentity(resolveMonorepoRoot(cwd))
	if err != nil {
		return ""
	}
	layout := homunculus.NewLayout()
	if err := layout.EnsureProjectDirs(ident.ID); err != nil {
		return ""
	}
	return layout.ObservationsFile(ident.ID)
}

// resolveMonorepoRoot mirrors threecorp's detect-project-wrapper.sh so
// every sub-repo / worktree session pools into the one monorepo project:
// a session inside a worktree resolves to the monorepo parent (the path
// before /.worktrees/); otherwise it walks up to the nearest ancestor
// holding the monorepo marker (.bough.yaml); else it falls back to cwd.
func resolveMonorepoRoot(cwd string) string {
	if i := strings.Index(cwd, "/.worktrees/"); i >= 0 {
		return cwd[:i]
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, ".bough.yaml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}

// maxObsBytes bounds the live observations file before it is archived,
// matching ECC observe.sh's 10 MiB threshold.
const maxObsBytes = 10 << 20

// rotateIfLarge archives the observations file when it exceeds
// maxObsBytes (ECC observe.sh:236-243): the live file the observer
// tails stays bounded, and older observations move to
// observations.archive/ rather than growing one file without limit.
// Best-effort — any error just means the file keeps growing a little.
func rotateIfLarge(obsPath string) {
	fi, err := os.Stat(obsPath)
	if err != nil || fi.Size() < maxObsBytes {
		return
	}
	archiveDir := filepath.Join(filepath.Dir(obsPath), "observations.archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return
	}
	dst := filepath.Join(archiveDir, fmt.Sprintf("observations-%d.jsonl", time.Now().UTC().UnixNano()))
	_ = os.Rename(obsPath, dst)
}

// dispatchInjectContext prints the confidence-ranked instinct block
// to the hook's stdout for the UserPromptSubmit event. Resolution +
// selection errors are swallowed (= a non-git directory or empty
// corpus must not break the operator's prompt); the block is only
// emitted when there is something worth injecting.
func dispatchInjectContext(c *cobra.Command) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	ident, err := homunculus.DetectIdentity(cwd)
	if err != nil {
		return
	}
	layout := homunculus.NewLayout()
	project, _ := homunculus.ScanInstincts(layout.InstinctsDir(ident.ID))
	global, _ := homunculus.ScanInstincts(layout.GlobalInstinctsDir())
	block, n := inject.Build(project, global, inject.Options{})
	if n == 0 {
		return
	}
	fmt.Fprint(c.OutOrStdout(), block)
}

// dispatchQualityGates loads .bough.yaml's quality_gates: section
// (when present) and runs the entries whose matchers fit the
// current event. Configuration absence is a hard non-error: a
// monorepo with no gates declared sees no behaviour change.
func dispatchQualityGates(c *cobra.Command, event string, payload []byte) {
	cfgPath := os.Getenv("BOUGH_CONFIG")
	if cfgPath == "" {
		cfgPath = ".bough.yaml"
	}
	if _, err := os.Stat(cfgPath); err != nil {
		return // no .bough.yaml in cwd → nothing to run.
	}
	cfg, err := loadConfigQuiet(cfgPath)
	if err != nil || len(cfg.QualityGates) == 0 {
		return
	}
	mc := buildMatchContext(event, payload)
	gates := convertGates(cfg.QualityGates)
	_ = qualitygate.RunMatching(commandCtx(c), gates, mc, c.ErrOrStderr())
}

// buildMatchContext projects the Claude Code hook payload into a
// qualitygate.MatchContext. The payload shape varies by event;
// PreToolUse / PostToolUse carry tool_name + tool_input. Missing
// fields fall through as the empty string so an unmatcher matcher
// still wildcards correctly.
func buildMatchContext(event string, payload []byte) qualitygate.MatchContext {
	mc := qualitygate.MatchContext{Event: event}
	if len(payload) == 0 {
		return mc
	}
	var probe struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return mc
	}
	mc.Tool = probe.ToolName
	if len(probe.ToolInput) == 0 {
		return mc
	}
	var ti struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Command  string `json:"command"`
	}
	_ = json.Unmarshal(probe.ToolInput, &ti)
	mc.FilePath = ti.FilePath
	if mc.FilePath == "" {
		mc.FilePath = ti.Path
	}
	mc.Command = ti.Command
	return mc
}

func convertGates(cfgs []config.QualityGateCfg) []qualitygate.Gate {
	out := make([]qualitygate.Gate, 0, len(cfgs))
	for _, g := range cfgs {
		out = append(out, qualitygate.Gate{
			Name:           g.Name,
			Command:        g.Command,
			OnEvent:        g.OnEvent,
			OnTool:         g.OnTool,
			OnMatch:        g.OnMatch,
			OnRepo:         g.OnRepo,
			TimeoutSeconds: g.TimeoutSeconds,
		})
	}
	return out
}

// loadConfigQuiet reads .bough.yaml without raising config drift
// warnings to stderr (= hook handle stderr is reserved for the
// quality-gate run summary; config noise would break the
// dispatcher's pass/fail signal).
func loadConfigQuiet(path string) (*config.Config, error) {
	return config.Load(path)
}

// HookScope picks which Claude Code settings.json the hook
// subcommands target. v0.8 (= P6) adds the global scope so an
// operator can wire bough's observer once at the user level rather
// than per-monorepo.
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

// claudeSettingsPath resolves the settings.json bough manages for
// the requested scope. Project scope anchors against the current
// working directory; user scope expands ~/.claude/settings.json.
func claudeSettingsPath(scope HookScope) (string, error) {
	switch scope {
	case "", HookScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getwd: %w", err)
		}
		return filepath.Join(cwd, ".claude", "settings.json"), nil
	case HookScopeUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("UserHomeDir: %w", err)
		}
		return filepath.Join(home, ".claude", "settings.json"), nil
	default:
		return "", fmt.Errorf("unknown hook scope %q (use 'project' or 'user')", scope)
	}
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
