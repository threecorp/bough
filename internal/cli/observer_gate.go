package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/instinctgate"
	"github.com/ikeikeikeike/bough/internal/provider/claudecli"
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
// personalDir is where the promoted corpus lives. It is consulted only to
// carry an existing instinct's first-seen stamp forward onto the re-mint;
// nothing is read from it for screening.
func stageInstincts(stagingDir, personalDir string, ident homunculus.ProjectIdentity, parsed map[string]any, now time.Time) ([]stagedInstinct, int, []error) {
	staged := []stagedInstinct{}
	skipped := 0
	errs := []error{}
	if parsed == nil {
		// No mint call was made this pass — the run is here only to screen
		// what a previous one left staged. That is not a malformed response;
		// reporting it as one puts a soft error on a clean path.
		return nil, 0, nil
	}
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
		if personalDir != "" {
			carryForwardFirstSeen(filepath.Join(personalDir, in.ID+".md"), in)
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

// adoptStrandedStaged returns staged instincts left behind by a PREVIOUS
// run so this pass screens them again.
//
// Without it, `.staging` is write-only: the promote loop skips a mint
// whose prior version could not be archived, and nothing ever looks at
// the directory again — `ScanInstincts` skips it by design, the doctor
// counts only quarantine, and neither REPORT.md restore path knows about
// it. The mint would be stranded forever, which is the "a silent hold is
// indistinguishable from data loss" failure the whole move-never-delete
// design exists to prevent.
//
// Ids this pass already re-minted are skipped: the fresh file has already
// replaced the stale one on disk, and adopting both would screen the same
// id twice in one batch.
func adoptStrandedStaged(stagingDir string, fresh []stagedInstinct) ([]stagedInstinct, []error) {
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		return nil, nil // no staging dir yet ⇒ nothing stranded
	}
	minted := make(map[string]bool, len(fresh))
	for _, s := range fresh {
		minted[s.in.ID] = true
	}
	var adopted []stagedInstinct
	var errs []error
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".md")
		if minted[id] {
			continue
		}
		path := filepath.Join(stagingDir, e.Name())
		in, rerr := homunculus.ReadInstinctFile(path)
		if rerr != nil || in == nil {
			errs = append(errs, fmt.Errorf("staging leftover %s is unreadable and stays staged: %v", e.Name(), rerr))
			continue
		}
		// The raw action string is not persisted separately, so it is
		// recovered from the body. It must be the WHOLE action block, not
		// its first line: buildInstinctBody writes multi-line actions
		// verbatim, and screening only line 1 would hand both the pattern
		// layer and the judge a truncated surface — an instruction to merge
		// on line 2 would be caught on the first pass and invisible here.
		adopted = append(adopted, stagedInstinct{in: in, action: actionBlock(in.Body), path: path})
	}
	return adopted, errs
}

// withoutIDs drops every candidate whose id is in remove, filtering in
// place. One helper rather than the same six lines per reason, so a fix
// to the compaction cannot land on only one of the paths — they are
// exercised by different tests, and a half-applied fix would go unnoticed.
func withoutIDs(cands []instinctgate.Candidate, remove []string) []instinctgate.Candidate {
	if len(remove) == 0 {
		return cands
	}
	drop := make(map[string]bool, len(remove))
	for _, id := range remove {
		drop[id] = true
	}
	kept := cands[:0]
	for _, c := range cands {
		if !drop[c.ID] {
			kept = append(kept, c)
		}
	}
	return kept
}

// actionBlock returns everything under "## Action" up to the next heading
// — the exact inverse of buildInstinctBody. Falls back to the whole body
// when the heading is absent, because screening too MUCH is safe and
// screening too little is the failure being avoided.
func actionBlock(body string) string {
	lines := strings.Split(body, "\n")
	start := -1
	for i, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l), "## Action") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return strings.TrimSpace(body)
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "## ") {
			end = i
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
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
	// ReviewCancelled says the unreviewed candidates were left unjudged by
	// an interrupt, not by the judge failing. They are NOT promoted.
	ReviewCancelled bool
	// ReviewCapped says the unreviewed candidates ran out of SELF-DoS
	// BUDGET rather than hitting a model outage. The distinction is the
	// whole point: "unreviewed=4" reads as "the model was down" when the
	// real cause is a cap the operator set and can raise. Candidates and
	// Votes carry the arithmetic so the report can name the value that
	// would have covered the batch.
	ReviewCapped     bool
	ReviewCandidates int
	ReviewVotes      int
	// JudgeOff says the LLM layer was requested but could not be built, so
	// nothing was judged. Without it a pass that never ran the judge is
	// indistinguishable on stdout from one where the judge found nothing.
	JudgeOff       bool
	JudgeOffReason string
	// JudgeProvider is the judge's provider, so the caller can print ITS
	// limiter snapshot. The judge holds a budget separate from minting;
	// reporting only the minting one understates what the pass spent.
	JudgeProvider *claudecli.Provider
	Errs          []error
}

// judgeFactory builds the LLM reviewer once the CANDIDATE COUNT is known,
// so the judge's budget can be sized to the batch instead of a fixed
// number that truncates it mid-review.
//
// Measured on the real binary: with the provider default (10 calls) and
// 3 votes, a 7-candidate batch judged 3 and admitted the remaining 4
// unjudged — including a prose-shaped violation that the judge quarantines
// correctly when the budget covers it. A batch of 4+ judge candidates is
// the ordinary case for a mint, so a fixed cap made the layer unreliable
// exactly when it had the most to check.
//
// nil disables the LLM layer.
type judgeFactory func(candidates int) (*instinctgate.Reviewer, *claudecli.Provider, error)

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
// ctx is the caller's cancellation scope — the interrupt-derived context
// cobra hands the command. It must be threaded here rather than replaced
// with context.Background(): the judge fan-out blocks on it, so with a
// detached context Ctrl-C did nothing for as long as the provider timeout
// allowed while the run kept spending the operator's budget.
func screenAndPromote(ctx context.Context, layout homunculus.Layout, projectID string, staged []stagedInstinct, gate *instinctgate.Gate, newJudge judgeFactory, now time.Time) promoteOutcome {
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
	if newJudge != nil && len(res.Cleared) > 0 {
		reviewer, judgeProv, jerr := newJudge(len(res.Cleared))
		out.JudgeProvider = judgeProv
		switch {
		case jerr != nil:
			out.JudgeOff, out.JudgeOffReason = true, jerr.Error()
		case reviewer == nil:
			out.JudgeOff, out.JudgeOffReason = true, "reviewer unavailable"
		}
		if reviewer != nil {
			out.ReviewCandidates, out.ReviewVotes = len(res.Cleared), reviewer.Votes
			br := reviewer.ReviewBatch(ctx, res.Cleared)
			out.Reviewed, out.ReviewFailed = br.Reviewed, br.Failed
			out.ReviewCancelled = br.Cancelled
			// The self-DoS cap is the operator's own setting, so exhausting it
			// is a different fact from the model being unreachable — and the
			// only one of the two they can act on.
			// Scan EVERY vote error: which one happened to be first is an
			// artefact of goroutine indexing, and a budget trip on vote 1
			// hidden behind a transient failure on vote 0 would be reported
			// as a model outage — the one cause the operator cannot fix.
			out.ReviewCapped = slices.ContainsFunc(br.Errs, func(e error) bool {
				return errors.Is(e, claudecli.ErrSelfDoSLimit)
			})
			switch {
			case br.Cancelled:
				out.Errs = append(out.Errs, fmt.Errorf("judge: interrupted with %d candidate(s) unreviewed — they stay staged, not promoted: %w", br.Failed, br.FirstErr))
			case out.ReviewCapped:
				out.Errs = append(out.Errs, fmt.Errorf("judge: self-DoS budget exhausted after %d of %d candidate(s): %w", br.Reviewed, out.ReviewCandidates, br.FirstErr))
			case br.FirstErr != nil:
				out.Errs = append(out.Errs, fmt.Errorf("judge: %d candidate(s) unreviewed (failed open): %w", br.Failed, br.FirstErr))
			}
			// Neither an interrupt nor an exhausted budget is a verdict.
			// Fail-open covers a model that could not ANSWER; it must not
			// cover a run the operator stopped, or one that ran out of the
			// operator's own call budget. Both leave the candidates staged,
			// which is what makes "re-run to review the rest" true — the
			// next pass adopts them (adoptStrandedStaged) and judges them.
			// Promoting them here would make that instruction a lie: nothing
			// ever re-screens a file already in the personal corpus.
			if br.Cancelled || out.ReviewCapped {
				res.Cleared = withoutIDs(res.Cleared, br.Unreviewed)
			}
			if len(br.Held) > 0 {
				heldIDs := make([]string, 0, len(br.Held))
				for _, d := range br.Held {
					heldIDs = append(heldIDs, d.ID)
				}
				res.Cleared = withoutIDs(res.Cleared, heldIDs)
				res.Held = append(res.Held, br.Held...)
			}
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
// DIFFERENT knowledge. It returns nil when there is nothing at dst (a
// fresh instinct) or when the two say the same thing (a re-mint that
// reinforces rather than supersedes — archiving those would bury the real
// changes in noise).
//
// "The same thing" is decided on CONTENT, not on bytes. Every rendered
// instinct embeds first_seen / last_seen stamped at mint time, so a byte
// comparison reports every re-mint as a change: measured against the real
// binary, an unchanged instinct was archived on every pass, and the whole
// corpus accumulated a copy per run — exactly the noise the "identical
// re-mints are NOT archived" line in the archive REPORT promises to avoid.
func archiveIfSuperseded(layout homunculus.Layout, projectID, id, dst, srcPath string, now time.Time) (*movedRecord, error) {
	priorInstinct, err := homunculus.ReadInstinctFile(dst)
	switch {
	// errors.Is, not os.IsNotExist: ReadInstinctFile WRAPS the syscall
	// error, and os.IsNotExist does not unwrap. Using it here silently
	// classified "file absent" as "file unreadable", which turned every
	// first mint of an id into a refusal to promote.
	case errors.Is(err, os.ErrNotExist):
		return nil, nil // fresh instinct: nothing to supersede
	case err != nil:
		// The file EXISTS and could not be read (permission, transient IO,
		// a lock) — or it parses as something this code does not model.
		// Treating that as "absent" would let the caller's rename overwrite
		// a prior version that was never archived: the silent destruction
		// this whole function exists to prevent. Surface it and let the
		// caller skip the promotion instead.
		return nil, fmt.Errorf("archive: read prior %s: %w", id, err)
	}
	if incoming, ierr := homunculus.ReadInstinctFile(srcPath); ierr == nil && sameKnowledge(priorInstinct, incoming) {
		return nil, nil // reinforcement, not a supersede
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

// sameKnowledge reports whether two versions of an instinct say the same
// thing. It deliberately ignores the volatile bookkeeping — FirstSeen,
// LastSeen, Observed — because those change on every mint and comparing
// them makes "unchanged" impossible to observe.
//
// Confidence IS compared: it is the value the session evaluator adjusts,
// and a confidence move is a real change to what the corpus asserts.
func sameKnowledge(a, b *homunculus.Instinct) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ID == b.ID &&
		a.Trigger == b.Trigger &&
		a.Body == b.Body &&
		a.Domain == b.Domain &&
		a.Scope == b.Scope &&
		a.Confidence == b.Confidence
}

// carryForwardFirstSeen preserves the ORIGINAL first-seen stamp when an id
// is re-minted. mapToInstinct stamps both timestamps with the mint time, so
// without this an instinct observed for months reports as first seen today
// — the provenance an operator uses to judge whether a note has earned its
// place is silently rewritten on every pass.
//
// Missing or unreadable prior version ⇒ leave the fresh stamp alone; this
// is provenance repair, not a precondition for minting.
func carryForwardFirstSeen(dst string, in *homunculus.Instinct) {
	prior, err := homunculus.ReadInstinctFile(dst)
	if err != nil || prior == nil || prior.FirstSeen.IsZero() {
		return
	}
	if in.FirstSeen.IsZero() || prior.FirstSeen.Before(in.FirstSeen) {
		in.FirstSeen = prior.FirstSeen
	}
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

// hasStaged reports whether a previous run left anything in the staging
// directory. It is a cheap existence probe, not a count: the caller only
// needs to know whether skipping the mint call would also skip real work.
func hasStaged(stagingDir string) bool {
	ents, err := os.ReadDir(stagingDir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			return true
		}
	}
	return false
}
