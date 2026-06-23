package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/bootstrap"
	"github.com/ikeikeikeike/bough/internal/evolve"
	"github.com/ikeikeikeike/bough/internal/judge"
	"github.com/ikeikeikeike/bough/pkg/schema"
	capapi "github.com/ikeikeikeike/bough/plugins/capability/api"
)

// newBootstrapCmd wires `bough bootstrap` — the v0.7.0 safety floor
// gains the v0.7.1 --apply path. Default is still dry-run (= round
// 5 reviewer non-negotiable: silent CLAUDE.md overwrite is the
// highest blast-radius failure mode); --apply opts into the
// pipeline that runs the 4-gate Go port + LLM judge and writes
// PASS candidates into .claude/skills/<label>.md.
//
// Backend defaults:
//
//	--judge=heuristic   (= LLM-free, deterministic, zero per-call cost)
//	--judge=replay      (= fixture playback from .evolve/judgements/)
//	--judge=claude      (= stub in v0.7.1; live in v0.7.2)
//
// The --force flag bypasses both the verdict gate (= DOUBT promotes
// alongside PASS) and the git-clean check on .claude/. FAIL stays
// skipped even with --force.
//
// The v0.7.0 dry-run reads raw observations from
// `.bough/observations.jsonl` (= the file `bough hook handle`
// writes from O-1.6), groups them by event + tool, and writes one
// Markdown file per group plus a _manifest.md index. v0.7.1 reuses
// the same observation log for --apply.
func newBootstrapCmd() *cobra.Command {
	var (
		dryRun        bool
		apply         bool
		force         bool
		judgeBackend  string
		promptVersion string
		modelID       string
		outDir        string
		obsLog        string
		auditDir      string
	)
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Generate candidate artifacts (memory / rule / skill / command / tool / agent / evaluator) from observations",
		Long: `bough bootstrap synthesises CapabilityArtifact candidates
from the raw event log Claude Code hooks produce.

Default (= no flag): the dry-run path writes Markdown proposals
under .bough/proposals/<timestamp>/ so the operator can git-diff
the output before any row reaches the memory backend or the
.claude/ tree.

--apply (= v0.7.1): runs the 4-gate Go port + JudgeClient pipeline
and writes PASS candidates into .claude/skills/<label>.md atomically.
The git status check refuses to overwrite uncommitted .claude/
edits; --force overrides.`,
		RunE: func(c *cobra.Command, _ []string) error {
			if apply && dryRun {
				return fmt.Errorf("--apply and --dry-run are mutually exclusive")
			}
			if apply {
				return runApply(c.Context(), c.OutOrStdout(), applyConfig{
					Backend:       judgeBackend,
					PromptVersion: promptVersion,
					ModelID:       modelID,
					ObsLog:        obsLog,
					AuditDir:      auditDir,
					Force:         force,
				})
			}
			ts := time.Now().UTC().Format("20060102T150405Z")
			target := filepath.Join(outDir, ts)
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
			summary, err := summariseObservations(obsLog)
			if err != nil {
				return err
			}
			manifest := buildManifest(ts, target, obsLog, summary)
			manifestPath := filepath.Join(target, "_manifest.md")
			if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
				return fmt.Errorf("write manifest %s: %w", manifestPath, err)
			}
			fmt.Fprintf(c.OutOrStdout(), "wrote %s\n", manifestPath)
			for event, count := range summary.PerEvent {
				if count == 0 {
					continue
				}
				groupFile := filepath.Join(target, fmt.Sprintf("%s.md", event))
				body := fmt.Sprintf(
					"# %s candidates (dry-run)\n\nObservations counted: %d\n\nv0.7.1 fills this file with the per-observation\nCandidate rule the LLM judge minted; v0.7.0 reports the count so\nan operator can verify the observer is capturing what they\nexpect before the live path turns on.\n",
					event, count,
				)
				if err := os.WriteFile(groupFile, []byte(body), 0o644); err != nil {
					return fmt.Errorf("write %s: %w", groupFile, err)
				}
				fmt.Fprintf(c.OutOrStdout(), "wrote %s\n", groupFile)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "write candidate artifacts to .bough/proposals/<ts>/*.md instead of the memory backend (v0.7.0 default; v0.7.1 keeps the path for back-compat)")
	cmd.Flags().BoolVar(&apply, "apply", false, "v0.7.1: run the 4-gate + judge pipeline and write PASS candidates into .claude/skills/<label>.md")
	cmd.Flags().BoolVar(&force, "force", false, "with --apply: promote DOUBT verdicts + bypass the .claude/ git-clean check (FAIL stays skipped)")
	cmd.Flags().StringVar(&judgeBackend, "judge", "heuristic", "JudgeClient backend: heuristic (default, LLM-free) | replay (.evolve fixtures) | claude (v0.7.2)")
	cmd.Flags().StringVar(&promptVersion, "prompt-version", "v3-2026-06-23", "prompt template version key — bump on prompt change")
	cmd.Flags().StringVar(&modelID, "model", "claude-opus-4-7", "model id stamped into the audit record (= reproducibility key)")
	cmd.Flags().StringVar(&outDir, "out-dir", ".bough/proposals", "parent directory for the per-run proposals subdirectory (dry-run only)")
	cmd.Flags().StringVar(&obsLog, "observations", ".bough/observations.jsonl", "raw observation log path (= the file `bough hook handle` writes; defaults to .bough/observations.jsonl)")
	cmd.Flags().StringVar(&auditDir, "audit-dir", ".evolve", "root for audit records + judge replay fixtures (cache_key.json under judgements/)")
	return cmd
}

// applyConfig pins the flag set runApply consumes.
type applyConfig struct {
	Backend       string
	PromptVersion string
	ModelID       string
	ObsLog        string
	AuditDir      string
	Force         bool
}

// runApply is the v0.7.1 --apply path. The implementation lives
// here (not in internal/bootstrap/) so the CLI surface stays the
// single source of truth for flags + defaults.
func runApply(ctx context.Context, stdout interface{ Write(p []byte) (n int, err error) }, cfg applyConfig) error {
	if ctx == nil {
		ctx = context.Background()
	}
	bundles, err := loadObservationsAsBundles(cfg.ObsLog)
	if err != nil {
		return err
	}
	if len(bundles) == 0 {
		fmt.Fprintln(stdout, "no observations to apply (run `bough hook install` and exercise the worktree first)")
		return nil
	}
	inner, err := selectJudge(cfg.Backend, cfg.ModelID, cfg.AuditDir)
	if err != nil {
		return err
	}
	audit := evolve.NewAuditDir(cfg.AuditDir)
	cached := evolve.NewCachedJudge(inner, audit)
	pipe := evolve.Pipeline{
		Judge:         cached,
		PromptVersion: cfg.PromptVersion,
		ModelID:       cfg.ModelID,
	}
	res, err := pipe.Run(ctx, bundles)
	if err != nil {
		return fmt.Errorf("evolve pipeline: %w", err)
	}
	monorepoRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	appliedRes, err := bootstrap.Apply(ctx, res, bootstrap.ApplyOptions{
		MonorepoRoot: monorepoRoot,
		Force:        cfg.Force,
		Stdout:       stdout,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "promoted=%d demoted=%d skipped=%d\n", appliedRes.Promoted, appliedRes.Demoted, len(appliedRes.Skipped))
	for _, f := range appliedRes.WrittenFiles {
		fmt.Fprintf(stdout, "  wrote %s\n", f)
	}
	if appliedRes.Diff != "" {
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Diff summary:")
		fmt.Fprintln(stdout, appliedRes.Diff)
	}
	return nil
}

// selectJudge wires the requested backend behind the JudgeClient
// interface. Replay defaults to non-strict so a cache miss falls
// through to the inner pipeline behaviour, not a hard error.
func selectJudge(backend, modelID, auditRoot string) (capapi.JudgeClient, error) {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "", "heuristic":
		return judge.NewHeuristicJudgeClient(), nil
	case "replay":
		return judge.NewReplayJudgeClient(filepath.Join(auditRoot, "judgements"), false), nil
	case "claude":
		return judge.NewClaudeJudgeClient(modelID), nil
	default:
		return nil, fmt.Errorf("--judge %q: unknown backend (use heuristic / replay / claude)", backend)
	}
}

// loadObservationsAsBundles reads .bough/observations.jsonl and
// projects each row into a TraceBundle. The schema matches the
// shape `bough hook handle` writes: {ts, event, payload}. Empty
// content rows are dropped on Gate 1 anyway, but the projection
// preserves them so the audit log records the drop reason.
func loadObservationsAsBundles(path string) ([]schema.TraceBundle, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	out := []schema.TraceBundle{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	i := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec struct {
			TS      string          `json:"ts"`
			Event   string          `json:"event"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339Nano, rec.TS)
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		i++
		out = append(out, schema.TraceBundle{
			ID:         fmt.Sprintf("obs-%d", i),
			Source:     schema.TraceSourceSessionLog,
			Scope:      schema.Scope{Level: schema.ScopeWorktree},
			Content:    string(rec.Payload),
			CapturedAt: ts,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return out, nil
}

// observationSummary aggregates the raw observations.jsonl file
// into per-event counts so the manifest can render a "what the
// observer saw" line without dumping the full file.
type observationSummary struct {
	Total    int
	PerEvent map[string]int
	Present  bool
}

func summariseObservations(path string) (observationSummary, error) {
	sum := observationSummary{PerEvent: map[string]int{}}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sum, nil
		}
		return sum, fmt.Errorf("open observations %s: %w", path, err)
	}
	defer f.Close()
	sum.Present = true
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		var event string
		if raw, ok := rec["event"]; ok {
			_ = json.Unmarshal(raw, &event)
		}
		if event == "" {
			continue
		}
		sum.Total++
		sum.PerEvent[event]++
	}
	if err := scanner.Err(); err != nil {
		return sum, fmt.Errorf("scan observations: %w", err)
	}
	return sum, nil
}

func buildManifest(ts, target, obsLog string, sum observationSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# bough bootstrap proposals — %s (dry-run)\n\n", ts)
	fmt.Fprintf(&b, "Output directory: `%s`\n\n", target)
	fmt.Fprintf(&b, "Observation source: `%s`", obsLog)
	if !sum.Present {
		fmt.Fprintf(&b, " (absent on disk; the dry-run still runs so the proposal shape is visible)")
	}
	fmt.Fprintf(&b, "\n\n")
	fmt.Fprintf(&b, "## Summary\n\n")
	if sum.Total == 0 {
		fmt.Fprintf(&b, "No observations were available for this dry-run. v0.7.0 ships the proposal\nlayer skeleton + the approval flow shape; the live observer that fills\n`.bough/observations.jsonl` wires in O-1.6, and the LLM-judge candidate\ngeneration that fills the per-event group files wires in v0.7.1.\n\n")
	} else {
		fmt.Fprintf(&b, "Observations counted: **%d**\n\n", sum.Total)
		fmt.Fprintf(&b, "| Event | Count |\n|---|---:|\n")
		for event, count := range sum.PerEvent {
			fmt.Fprintf(&b, "| %s | %d |\n", event, count)
		}
		fmt.Fprintf(&b, "\n")
	}
	fmt.Fprintf(&b, "## How to approve\n\n")
	fmt.Fprintf(&b, "The v0.7.1 release ships `bough instinct approve --from <proposals-dir>`\nso an operator can review the per-event Markdown files (= one per\nartifact kind the LLM judge proposed) and promote the ones they want\ninto the memory backend.\n\nFor v0.7.0 the proposals layer is informational only — the per-event\nfiles describe the observations the agent saw, not yet the candidate\nrules the agent minted from them.\n")
	return b.String()
}
