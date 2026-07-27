package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/instinctgate"
)

// stagedInstinct is one minted instinct written to the staging dir but
// not yet promoted. It carries the raw action string alongside the
// rendered Instinct because the policy gate scans the propagating
// surface (trigger + action) rather than the whole body — a note may
// legitimately cite a forbidden command in its evidence without
// recommending it, and the folded-into-body form would lose that split.
type stagedInstinct struct {
	in     *homunculus.Instinct
	action string
	path   string
}

// stageInstincts parses the LLM result, runs the host-side safety
// checks, and writes every accepted instinct to stagingDir (NOT the
// personal corpus). Staging is a sibling of the personal dir that
// ScanInstincts never descends into, so a minted note is invisible to
// injection until screenAndPromote moves it — this closes the
// live-before-check window structurally, independent of whether the
// gate is enabled. Returns the staged items, the skipped count, and
// per-entry soft errors.
func stageInstincts(stagingDir string, ident homunculus.ProjectIdentity, parsed map[string]any, now time.Time) ([]stagedInstinct, int, []error) {
	staged := []stagedInstinct{}
	skipped := 0
	errs := []error{}
	raw, ok := parsed["instincts"].([]any)
	if !ok {
		return nil, 0, []error{fmt.Errorf("response missing 'instincts' array (got %T)", parsed["instincts"])}
	}
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			skipped++
			errs = append(errs, fmt.Errorf("entry was not an object: %T", item))
			continue
		}
		in, err := mapToInstinct(entry, ident, now)
		if err != nil {
			skipped++
			errs = append(errs, err)
			continue
		}
		if rule, err := checkInstinctSafety(in); err != nil {
			skipped++
			errs = append(errs, fmt.Errorf("instinct %q failed safety check (%s): %w", in.ID, rule, err))
			continue
		}
		path, werr := homunculus.WriteInstinctFile(stagingDir, in)
		if werr != nil {
			skipped++
			errs = append(errs, werr)
			continue
		}
		action, _ := entry["action"].(string)
		staged = append(staged, stagedInstinct{in: in, action: action, path: path})
	}
	return staged, skipped, errs
}

// promoteOutcome is the result of screening one staged batch. Emitted
// were cleared and moved to the personal corpus; Quarantined were held
// and moved to a dated batch under the quarantine dir with a REPORT.
// BatchDir is empty when nothing was held.
type promoteOutcome struct {
	Emitted     int
	Quarantined int
	BatchDir    string
	Errs        []error
}

// heldRecord is one quarantined instinct: what rule held it and where
// the file now lives, so the REPORT can print a restore one-liner.
type heldRecord struct {
	id   string
	rule string
	path string
}

// screenAndPromote runs the policy gate over a staged batch and applies
// its decision by moving files: cleared instincts are promoted (renamed)
// into the personal corpus, held ones are quarantined (renamed) into a
// dated batch dir with a REPORT. Every transition is a move, never a
// delete, so a held instinct is always recoverable — a false hold costs
// an operator glance, not data.
func screenAndPromote(layout homunculus.Layout, projectID string, staged []stagedInstinct, gate *instinctgate.Gate, now time.Time) promoteOutcome {
	out := promoteOutcome{}
	if len(staged) == 0 {
		return out
	}
	byID := make(map[string]stagedInstinct, len(staged))
	cands := make([]instinctgate.Candidate, 0, len(staged))
	for _, s := range staged {
		byID[s.in.ID] = s
		cands = append(cands, instinctgate.Candidate{
			ID:      s.in.ID,
			Trigger: s.in.Trigger,
			Action:  s.action,
			Body:    s.in.Body,
			Path:    s.path,
		})
	}
	res := gate.Screen(cands)

	personalDir := layout.InstinctsDir(projectID)
	if err := os.MkdirAll(personalDir, 0o755); err != nil {
		out.Errs = append(out.Errs, fmt.Errorf("gate: mkdir personal %s: %w", personalDir, err))
		return out
	}
	for _, c := range res.Cleared {
		s := byID[c.ID]
		dst := filepath.Join(personalDir, c.ID+".md")
		if err := os.Rename(s.path, dst); err != nil {
			out.Errs = append(out.Errs, fmt.Errorf("gate: promote %s: %w", c.ID, err))
			continue
		}
		out.Emitted++
	}

	if len(res.Held) == 0 {
		return out
	}
	batchDir := filepath.Join(layout.QuarantineDir(projectID), now.Format("20060102-150405"))
	if err := os.MkdirAll(batchDir, 0o755); err != nil {
		out.Errs = append(out.Errs, fmt.Errorf("gate: mkdir quarantine %s: %w", batchDir, err))
		return out
	}
	held := make([]heldRecord, 0, len(res.Held))
	for _, d := range res.Held {
		s := byID[d.ID]
		dst := filepath.Join(batchDir, d.ID+".md")
		if err := os.Rename(s.path, dst); err != nil {
			out.Errs = append(out.Errs, fmt.Errorf("gate: quarantine %s: %w", d.ID, err))
			continue
		}
		held = append(held, heldRecord{id: d.ID, rule: d.Rule, path: dst})
		out.Quarantined++
	}
	if err := writeQuarantineReport(batchDir, held, personalDir, now); err != nil {
		out.Errs = append(out.Errs, err)
	}
	out.BatchDir = batchDir
	return out
}

// writeQuarantineReport writes a human REPORT.md for one quarantine
// batch. It is honest about scope on purpose: the banner states the gate
// matches command shapes only and makes NO completeness claim, because a
// prior incident hid a real coverage gap behind a confident "clean"
// summary. Each row carries a restore one-liner so putting a held
// instinct back is a copy-paste, not a puzzle.
func writeQuarantineReport(batchDir string, held []heldRecord, personalDir string, now time.Time) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Policy quarantine — %s\n\n", now.Format(time.RFC3339))
	b.WriteString("These instincts were HELD by the deterministic policy gate: their\n")
	b.WriteString("propagating surface (trigger + action) matched a command-shaped\n")
	b.WriteString("forbidden action. Nothing was deleted — each file was MOVED here\n")
	b.WriteString("whole and stays out of injection until you restore it. Review each\n")
	b.WriteString("one, then run the restore command in its row to put it back.\n\n")
	b.WriteString("Scope: this gate matches COMMAND SHAPES ONLY. It is NOT a completeness\n")
	b.WriteString("claim — prose-shaped intent is not screened here.\n\n")
	b.WriteString("| id | rule | restore |\n|---|---|---|\n")
	for _, h := range held {
		dst := filepath.Join(personalDir, h.id+".md")
		fmt.Fprintf(&b, "| %s | %s | `mv %s %s` |\n", h.id, h.rule, h.path, dst)
	}
	reportPath := filepath.Join(batchDir, "REPORT.md")
	if err := os.WriteFile(reportPath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("gate: write quarantine report %s: %w", reportPath, err)
	}
	return nil
}

// gateConfigFor builds the policy-gate config for the monorepo at root.
// A missing or unreadable .bough.yaml is not an error here: the gate is
// reversible, so the safe fallback is to run it (Enabled: true) rather
// than skip it. When the config loads, the operator's `instinct.gate`
// block decides — defaulting on when the block is absent (GateEnabled).
func gateConfigFor(cmd *cobra.Command, root string) instinctgate.Config {
	cfg, err := loadConfigQuiet(resolveConfigPath(cmd, root))
	if err != nil {
		return instinctgate.Config{Enabled: true}
	}
	return instinctgate.Config{
		Enabled:  cfg.Instinct.GateEnabled(),
		AllowIDs: cfg.Instinct.Gate.AllowIDs,
	}
}
