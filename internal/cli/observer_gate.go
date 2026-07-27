package cli

import (
	"bytes"
	"context"
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
	// Reviewed and ReviewFailed report the LLM layer's coverage. "0 held"
	// is meaningless without them: a judge that could not run at all
	// produces the same held count as a corpus with nothing wrong.
	Reviewed     int
	ReviewFailed int
	Errs         []error
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
func screenAndPromote(layout homunculus.Layout, projectID string, staged []stagedInstinct, gate *instinctgate.Gate, reviewer *instinctgate.Reviewer, now time.Time) promoteOutcome {
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
	// The LLM layer runs LAST, on what the deterministic layers already
	// cleared, so a model outage can never un-catch a command-shaped
	// violation. It fails open and reports, because a guard that silently
	// held everything would stop the corpus growing while looking like a
	// clean pass.
	if reviewer != nil && len(res.Cleared) > 0 {
		judged, reviewed, failed, jerr := reviewer.ReviewBatch(context.Background(), res.Cleared)
		out.Reviewed, out.ReviewFailed = reviewed, failed
		if jerr != nil {
			out.Errs = append(out.Errs, fmt.Errorf("judge: %d candidate(s) unreviewed (failed open): %w", failed, jerr))
		}
		if len(judged) > 0 {
			flagged := make(map[string]bool, len(judged))
			for _, d := range judged {
				flagged[d.ID] = true
			}
			kept := res.Cleared[:0]
			for _, c := range res.Cleared {
				if !flagged[c.ID] {
					kept = append(kept, c)
				}
			}
			res.Cleared = kept
			res.Held = append(res.Held, judged...)
		}
	}

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
		rec, err := archiveIfSuperseded(layout, projectID, c.ID, dst, s.path, now)
		if err != nil {
			// The prior version could not be archived, so promoting would
			// overwrite it unrecorded. Leave the staged file where it is —
			// it stays out of injection and can be promoted on the next run
			// once the archive succeeds. Losing a mint is recoverable;
			// losing the version it replaced is not.
			out.Errs = append(out.Errs, err)
			continue
		}
		if rec != nil {
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
	switch {
	case os.IsNotExist(err):
		return nil, nil // fresh instinct: nothing to supersede
	case err != nil:
		// The file EXISTS and could not be read (permission, transient IO,
		// a lock). Treating that as "absent" would let the caller's rename
		// overwrite a prior version that was never archived — the silent
		// destruction this whole function exists to prevent. Surface it and
		// let the caller skip the promotion instead.
		return nil, fmt.Errorf("archive: read prior %s: %w", id, err)
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
		return instinctgate.Config{
			Enabled:    true,
			Denylist:   loadDenylistQuiet(root, ""),
			Governance: instinctgate.LoadGovernance(governancePaths(root, nil)),
		}
	}
	return instinctgate.Config{
		Enabled:    cfg.Instinct.GateEnabled(),
		AllowIDs:   cfg.Instinct.Gate.AllowIDs,
		Denylist:   loadDenylistQuiet(root, cfg.Instinct.Gate.DenylistPath),
		Governance: instinctgate.LoadGovernance(governancePaths(root, cfg.Instinct.Gate.GovernancePaths)),
	}
}

// DefaultDenylistPath is where bough looks for the untracked denylist
// sidecar when the operator has not configured one. It sits under the
// repo's own .bough/ directory (gitignored) so the file lives beside the
// project it describes without ever being committed.
const DefaultDenylistPath = ".bough/denylist.txt"

// defaultGovernancePaths are the conventional locations of a project's
// rule documents. Both are checked because teams split governance
// differently, and grounding against only one would flag the other's
// rules as invented.
var defaultGovernancePaths = []string{"CLAUDE.md", ".claude/rules"}

// loadDenylistQuiet resolves and loads the denylist sidecar. A load
// error is downgraded to an inert list here rather than failing the
// mint: the observer runs unattended, and a broken sidecar must not cost
// the operator the whole extraction pass. `bough doctor` reports the
// resulting posture, so "off" stays visible.
func loadDenylistQuiet(root, configured string) *instinctgate.Denylist {
	path := configured
	if path == "" {
		path = DefaultDenylistPath
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	d, err := instinctgate.LoadDenylist(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bough: WARNING denylist %s unreadable (%v) — that layer is OFF for this pass\n", path, err)
		return nil
	}
	return d
}

// governancePaths resolves the rule-document locations against the
// monorepo root, falling back to the conventional set.
func governancePaths(root string, configured []string) []string {
	paths := configured
	if len(paths) == 0 {
		paths = defaultGovernancePaths
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		out = append(out, p)
	}
	return out
}
