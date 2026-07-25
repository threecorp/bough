package cli

import (
	"bytes"
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
	// Superseded counts promoted instincts that replaced an existing file
	// whose text differed; the prior versions were archived under
	// ArchiveDir rather than overwritten away.
	Superseded int
	ArchiveDir string
	Errs       []error
}

// movedRecord is one file this pass relocated instead of destroying:
// which instinct, why it moved, and where it now lives so a report can
// print the one-liner that puts it back.
type movedRecord struct {
	id     string
	reason string
	path   string
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
	superseded := []movedRecord{}
	for _, c := range res.Cleared {
		s := byID[c.ID]
		dst := filepath.Join(personalDir, c.ID+".md")
		// Minting is id-addressed and the writer overwrites in place, so
		// re-minting an existing id would destroy the previous version's
		// text with no record. Move the prior version aside first; an
		// identical re-mint (reinforcement) is not a supersede and is left
		// alone so the archive holds real changes only.
		if rec, err := archiveIfSuperseded(layout, projectID, c.ID, dst, s.path, now); err != nil {
			out.Errs = append(out.Errs, err)
		} else if rec != nil {
			superseded = append(superseded, *rec)
		}
		if err := os.Rename(s.path, dst); err != nil {
			out.Errs = append(out.Errs, fmt.Errorf("gate: promote %s: %w", c.ID, err))
			continue
		}
		out.Emitted++
	}
	if len(superseded) > 0 {
		out.Superseded = len(superseded)
		out.ArchiveDir = filepath.Dir(superseded[0].path)
		if err := writeMoveReport(out.ArchiveDir, archiveReportSpec(personalDir), superseded, now); err != nil {
			out.Errs = append(out.Errs, err)
		}
	}

	if len(res.Held) == 0 {
		return out
	}
	batchDir := filepath.Join(layout.QuarantineDir(projectID), now.Format("20060102-150405"))
	if err := os.MkdirAll(batchDir, 0o755); err != nil {
		out.Errs = append(out.Errs, fmt.Errorf("gate: mkdir quarantine %s: %w", batchDir, err))
		return out
	}
	held := make([]movedRecord, 0, len(res.Held))
	for _, d := range res.Held {
		s := byID[d.ID]
		dst := filepath.Join(batchDir, d.ID+".md")
		if err := os.Rename(s.path, dst); err != nil {
			out.Errs = append(out.Errs, fmt.Errorf("gate: quarantine %s: %w", d.ID, err))
			continue
		}
		held = append(held, movedRecord{id: d.ID, reason: d.Rule, path: dst})
		out.Quarantined++
	}
	if err := writeMoveReport(batchDir, quarantineReportSpec(personalDir), held, now); err != nil {
		out.Errs = append(out.Errs, err)
	}
	out.BatchDir = batchDir
	return out
}

// archiveIfSuperseded moves the instinct currently at dst into a dated
// archive batch when the incoming staged file at srcPath carries
// DIFFERENT text. It returns nil when there is nothing at dst (a fresh
// instinct) or when the two files are byte-identical (a re-mint that
// reinforces rather than supersedes — archiving those would bury the
// real changes in noise).
func archiveIfSuperseded(layout homunculus.Layout, projectID, id, dst, srcPath string, now time.Time) (*movedRecord, error) {
	prior, err := os.ReadFile(dst)
	if err != nil {
		return nil, nil //nolint:nilerr // absent (or unreadable) prior = nothing to supersede
	}
	incoming, err := os.ReadFile(srcPath)
	if err == nil && bytes.Equal(prior, incoming) {
		return nil, nil // identical re-mint: reinforcement, not a supersede
	}
	batchDir := filepath.Join(layout.ArchiveDir(projectID), now.Format("20060102-150405"))
	if err := os.MkdirAll(batchDir, 0o755); err != nil {
		return nil, fmt.Errorf("archive: mkdir %s: %w", batchDir, err)
	}
	archived := filepath.Join(batchDir, id+".md")
	if err := os.Rename(dst, archived); err != nil {
		return nil, fmt.Errorf("archive: move superseded %s: %w", id, err)
	}
	return &movedRecord{id: id, reason: "superseded by a newer mint", path: archived}, nil
}

// reportSpec is the per-batch prose of a move report: the title, the
// banner explaining what happened and why it is reversible, and the
// header of the "reason" column. Quarantine and archive differ only in
// this prose — the mechanism (files moved whole, one row each, a restore
// one-liner) is identical, so they share one writer.
type reportSpec struct {
	title       string
	banner      string
	reasonLabel string
	restoreDir  string
}

func quarantineReportSpec(personalDir string) reportSpec {
	return reportSpec{
		title: "Policy quarantine",
		banner: "These instincts were HELD by the deterministic policy gate: their\n" +
			"propagating surface (trigger + action) matched a command-shaped\n" +
			"forbidden action. Nothing was deleted — each file was MOVED here\n" +
			"whole and stays out of injection until you restore it. Review each\n" +
			"one, then run the restore command in its row to put it back.\n\n" +
			"Scope: this gate matches COMMAND SHAPES ONLY. It is NOT a completeness\n" +
			"claim — prose-shaped intent is not screened here.",
		reasonLabel: "rule",
		restoreDir:  personalDir,
	}
}

func archiveReportSpec(personalDir string) reportSpec {
	return reportSpec{
		title: "Superseded instincts",
		banner: "A newer mint replaced these instincts under the same id. Minting is\n" +
			"id-addressed and the writer overwrites in place, so the previous text\n" +
			"would otherwise have been destroyed silently. Nothing was deleted —\n" +
			"each prior version was MOVED here whole. Restoring one puts the older\n" +
			"text back and overwrites the newer version, so read both first.\n\n" +
			"Identical re-mints are NOT archived: only versions whose text actually\n" +
			"changed appear here.",
		reasonLabel: "reason",
		restoreDir:  personalDir,
	}
}

// writeMoveReport writes the REPORT.md for one batch of relocated
// instincts. Every row carries the restore one-liner so putting a file
// back is a copy-paste, not a puzzle — a move nobody can find is
// indistinguishable from a delete.
func writeMoveReport(batchDir string, spec reportSpec, records []movedRecord, now time.Time) error {
	if len(records) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s\n\n", spec.title, now.Format(time.RFC3339))
	b.WriteString(spec.banner)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "| id | %s | restore |\n|---|---|---|\n", spec.reasonLabel)
	for _, r := range records {
		dst := filepath.Join(spec.restoreDir, r.id+".md")
		fmt.Fprintf(&b, "| %s | %s | `mv %s %s` |\n", r.id, r.reason, r.path, dst)
	}
	reportPath := filepath.Join(batchDir, "REPORT.md")
	if err := os.WriteFile(reportPath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write report %s: %w", reportPath, err)
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
