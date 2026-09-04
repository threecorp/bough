package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/instinctgate"
	"github.com/ikeikeikeike/bough/internal/provider/claudecli"
)

// Promotion thresholds — faithful port of ECC continuous-learning-v2
// instinct-cli.py PROMOTE_MIN_PROJECTS / PROMOTE_CONFIDENCE_THRESHOLD.
// An instinct that independently reached >= promoteMinProjects projects
// with a mean confidence >= promoteMinConfidence is general enough to
// live in the global corpus (which inject layers into every project).
// Both are flag-overridable; the constants are the ECC defaults.
const (
	promoteMinProjects   = 2
	promoteMinConfidence = 0.8
)

type promoteOptions struct {
	minProjects   int
	minConfidence float64
	dryRun        bool
	// gate screens candidates before they reach global scope. nil means
	// no screening — used only by tests that are exercising the
	// thresholds rather than the guard.
	gate *instinctgate.Gate
	// newJudge builds the LLM layer for the candidates that cleared the
	// patterns. nil disables it (--judge=false, and the threshold tests).
	//
	// Unlike the mint path this one fails CLOSED. There, a judge that
	// cannot answer must promote and log: it runs on every session, and
	// blocking it stops the corpus growing. Promotion runs rarely, by
	// hand, and what it writes is injected into EVERY project forever —
	// so "the judge could not run" must not read as "clean".
	newJudge judgeFactory
	ctx      context.Context
}

// promoteEntry is one project's copy of a shared instinct id.
type promoteEntry struct {
	projectID   string
	projectName string
	instinct    *homunculus.Instinct
}

// promoteCandidate is a cross-project instinct id together with the
// per-project copies that voted for it.
type promoteCandidate struct {
	id            string
	avgConfidence float64
	entries       []promoteEntry
}

// promoteResult records what one promotion pass did, for the CLI
// summary. dryRun mirrors the request so the renderer can switch verbs.
type promoteResult struct {
	promoted      []promoteCandidate
	skippedGlobal []promoteCandidate // id already in the global corpus
	belowThresh   int                // cross-project but under the confidence threshold
	// gateHeld are candidates the policy gate refused to promote. They
	// are reported rather than counted: a silent refusal at the widest
	// blast radius in the system is the last place to hide a decision.
	gateHeld  []gateHold
	writeErrs []error
	// judgeErr stops the whole promotion: on this path a judge that
	// cannot answer refuses rather than waves through.
	judgeErr error
	dryRun   bool
}

// gateHold names one candidate the gate kept out of global scope.
type gateHold struct {
	id   string
	rule string
}

// screenPromotion runs the gate over one promotion candidate on the same
// surface the mint path screens: trigger plus the WHOLE action block. It
// screened only the first action line until it was measured, which meant
// an instruction to merge on line 2 was held on the way in and cleared on
// the way up — into global scope, which applies to every project. The
// weaker of two surfaces decides what the corpus ends up holding, so they
// have to be the same one.
func screenPromotion(gate *instinctgate.Gate, cand promoteCandidate) (instinctgate.Decision, bool) {
	if gate == nil {
		return instinctgate.Decision{}, false
	}
	best := bestEntry(cand.entries).instinct
	res := gate.Screen([]instinctgate.Candidate{{
		ID:      cand.id,
		Trigger: best.Trigger,
		Action:  actionBlock(best.Body),
		Body:    best.Body,
	}})
	if len(res.Held) == 1 {
		return res.Held[0], true
	}
	return instinctgate.Decision{}, false
}

// promoteInstincts scans every registered project's personal +
// inherited instincts, groups them by id, and promotes the ids that
// appear in >= opt.minProjects projects with mean confidence >=
// opt.minConfidence to the global corpus (layout.GlobalInstinctsDir).
// Source project instincts are left untouched (ECC parity); an id
// already present globally is skipped (idempotent). now stamps the
// promoted_date provenance so tests are deterministic.
func promoteInstincts(layout homunculus.Layout, opt promoteOptions, now time.Time) (promoteResult, error) {
	res := promoteResult{dryRun: opt.dryRun}
	var cleared []promoteCandidate

	reg, err := homunculus.NewRegistryRW(layout).Read()
	if err != nil {
		return res, fmt.Errorf("instinct promote: read registry: %w", err)
	}

	cross := groupCrossProject(layout, reg)

	// Already-global ids are skipped so re-running is idempotent.
	globalIns, _ := homunculus.ScanInstincts(layout.GlobalInstinctsDir())
	globalIDs := map[string]struct{}{}
	for _, in := range globalIns {
		globalIDs[in.ID] = struct{}{}
	}

	ids := make([]string, 0, len(cross))
	for id := range cross {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		entries := cross[id]
		if len(entries) < opt.minProjects {
			continue // single-project (or under --min-projects): not general
		}
		cand := promoteCandidate{id: id, avgConfidence: meanConfidence(entries), entries: entries}
		if _, isGlobal := globalIDs[id]; isGlobal {
			res.skippedGlobal = append(res.skippedGlobal, cand)
			continue
		}
		if cand.avgConfidence < opt.minConfidence {
			res.belowThresh++
			continue
		}
		// Promotion is the widest blast radius in the system: a global
		// instinct is injected into EVERY project, so a forbidden action
		// that reaches global scope is recommended everywhere at once.
		// Screen it with the same matcher the mint path uses — one
		// implementation, several call sites, so the two cannot drift into
		// disagreeing about what is allowed.
		if d, held := screenPromotion(opt.gate, cand); held {
			res.gateHeld = append(res.gateHeld, gateHold{id: cand.id, rule: d.Rule})
			continue
		}
		cleared = append(cleared, cand)
	}

	// The LLM layer runs on what the patterns cleared, in ONE batch:
	// the deterministic hold is already decided, and judging per
	// candidate would re-enter the limiter once per id.
	cleared, judgeHeld, judgeErr := judgePromotions(opt, cleared)
	res.gateHeld = append(res.gateHeld, judgeHeld...)
	if judgeErr != nil {
		// Fail-closed: nothing is promoted on a judge that could not
		// answer. The candidates stay where they are — this path writes,
		// it never removes — so the next run re-offers them.
		res.judgeErr = judgeErr
		return res, nil
	}

	for _, cand := range cleared {
		if !opt.dryRun {
			if err := writePromoted(layout, cand, now); err != nil {
				res.writeErrs = append(res.writeErrs, err)
				continue
			}
		}
		res.promoted = append(res.promoted, cand)
	}
	return res, nil
}

// judgePromotions runs the consensus judge over the candidates the
// patterns cleared. Returns the survivors, the holds, and — on a judge
// that could not be built or could not reach a verdict — an error that
// stops the promotion entirely.
func judgePromotions(opt promoteOptions, cleared []promoteCandidate) ([]promoteCandidate, []gateHold, error) {
	if opt.newJudge == nil || len(cleared) == 0 {
		return cleared, nil, nil
	}
	reviewer, _, err := opt.newJudge(len(cleared))
	if err != nil {
		return nil, nil, fmt.Errorf("the judge could not be built, and promotion writes to global scope: %w", err)
	}
	if reviewer == nil {
		return nil, nil, errors.New("the judge is unavailable, and promotion writes to global scope")
	}
	byID := make(map[string]promoteCandidate, len(cleared))
	cands := make([]instinctgate.Candidate, 0, len(cleared))
	for _, c := range cleared {
		byID[c.id] = c
		best := bestEntry(c.entries).instinct
		cands = append(cands, instinctgate.Candidate{
			ID:      c.id,
			Trigger: best.Trigger,
			Action:  actionBlock(best.Body),
			Body:    best.Body,
		})
	}
	ctx := opt.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	br := reviewer.ReviewBatch(ctx, cands)
	// A candidate the judge could not reach a verdict on is NOT promoted:
	// on this path an unanswered question is a refusal, not a pass.
	if br.Failed > 0 || br.Cancelled {
		return nil, nil, fmt.Errorf("the judge left %d of %d candidate(s) unanswered (cancelled=%v), and promotion writes to global scope",
			br.Failed, len(cands), br.Cancelled)
	}
	held := make(map[string]string, len(br.Held))
	var holds []gateHold
	for _, d := range br.Held {
		held[d.ID] = d.Rule
		holds = append(holds, gateHold{id: d.ID, rule: d.Rule})
	}
	survivors := make([]promoteCandidate, 0, len(cleared))
	for _, c := range cleared {
		if _, isHeld := held[c.id]; !isHeld {
			survivors = append(survivors, c)
		}
	}
	return survivors, holds, nil
}

// groupCrossProject maps instinct id -> the per-project copies of it,
// deduped within a project so an id present in both personal/ and
// inherited/ counts once (ECC seen_in_project parity).
func groupCrossProject(layout homunculus.Layout, reg map[string]homunculus.Project) map[string][]promoteEntry {
	cross := map[string][]promoteEntry{}
	pids := make([]string, 0, len(reg))
	for pid := range reg {
		pids = append(pids, pid)
	}
	sort.Strings(pids)
	for _, pid := range pids {
		name := reg[pid].Name
		seen := map[string]struct{}{}
		for _, dir := range []string{layout.InstinctsDir(pid), layout.InheritedDir(pid)} {
			ins, _ := homunculus.ScanInstincts(dir)
			for _, in := range ins {
				if _, dup := seen[in.ID]; dup {
					continue
				}
				seen[in.ID] = struct{}{}
				cross[in.ID] = append(cross[in.ID], promoteEntry{projectID: pid, projectName: name, instinct: in})
			}
		}
	}
	return cross
}

func meanConfidence(entries []promoteEntry) float64 {
	if len(entries) == 0 {
		return 0
	}
	var sum float64
	for _, e := range entries {
		sum += e.instinct.Confidence
	}
	return sum / float64(len(entries))
}

// bestEntry picks the highest-confidence copy of a shared instinct as
// the trigger/body source — ECC's conflict resolution for divergent
// bodies under the same id.
func bestEntry(entries []promoteEntry) promoteEntry {
	best := entries[0]
	for _, e := range entries[1:] {
		if e.instinct.Confidence > best.instinct.Confidence {
			best = e
		}
	}
	return best
}

// writePromoted writes the global copy of one candidate. confidence is
// the cross-project mean (ECC); the body/trigger/domain come from the
// highest-confidence source. bough enriches ECC's provenance frontmatter
// (source / promoted_date / seen_in_projects) with promoted_from +
// aggregated first_seen / observed so the global corpus is auditable.
func writePromoted(layout homunculus.Layout, cand promoteCandidate, now time.Time) error {
	best := bestEntry(cand.entries).instinct
	from := make([]string, 0, len(cand.entries))
	var earliest time.Time
	var observed int
	for _, e := range cand.entries {
		from = append(from, e.projectID)
		observed += e.instinct.Observed
		if fs := e.instinct.FirstSeen; !fs.IsZero() && (earliest.IsZero() || fs.Before(earliest)) {
			earliest = fs
		}
	}
	sort.Strings(from)
	if earliest.IsZero() {
		earliest = now.UTC()
	}
	out := &homunculus.Instinct{
		ID:         cand.id,
		Trigger:    best.Trigger,
		Confidence: cand.avgConfidence,
		Domain:     best.Domain,
		Scope:      "global",
		Observed:   observed,
		FirstSeen:  earliest,
		LastSeen:   now.UTC(),
		Body:       best.Body,
		// Provenance survives the write via renderInstinct's Raw merge.
		Raw: map[string]any{
			"source":           "auto-promoted",
			"promoted_date":    now.UTC().Format(time.RFC3339),
			"seen_in_projects": len(cand.entries),
			"promoted_from":    from,
		},
	}
	if _, err := homunculus.WriteInstinctFile(layout.GlobalInstinctsDir(), out); err != nil {
		return fmt.Errorf("instinct promote: write %s: %w", cand.id, err)
	}
	return nil
}

func newInstinctPromoteCmd() *cobra.Command {
	opt := promoteOptions{
		minProjects:   promoteMinProjects,
		minConfidence: promoteMinConfidence,
	}
	var (
		judge         = true
		model         string
		judgeMaxCalls int
	)
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Promote cross-project instincts into the global corpus",
		Long: `bough instinct promote scans every registered project's
instincts and copies the ones that independently reached multiple
projects with high confidence into the global corpus
(~/.local/share/bough-homunculus/instincts/personal), which inject
layers into every project. This mirrors ECC continuous-learning-v2's
auto-promotion: an id seen in >= --min-projects projects with mean
confidence >= --min-confidence is general enough to be global. Source
project instincts are left untouched and ids already global are skipped
(idempotent). Previews by default (--dry-run); pass --apply (or
--dry-run=false) to write into the global corpus.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Global scope reaches every project, so promotion is screened by
			// the same gate the mint path uses. Resolving it here (rather than
			// inside promoteInstincts) keeps that function a pure function of
			// its options, which is what the threshold tests rely on.
			cwd, _ := os.Getwd()
			gateCfg, forbidden, governance := gateSettings(cmd, resolveMonorepoRoot(cwd))
			opt.gate = instinctgate.New(gateCfg)
			opt.ctx = cmd.Context()
			// The judge runs here for the same reason the gate does:
			// global scope reaches every project. A dry run judges too —
			// a preview that skipped the expensive layer would advertise a
			// promotion the real run then refuses.
			if judge {
				opt.newJudge = func(candidates int) (*instinctgate.Reviewer, *claudecli.Provider, error) {
					budget := judgeMaxCalls
					if budget <= 0 {
						budget = min(candidates*instinctgate.DefaultVotes, judgeCallCeiling)
					}
					return newGateReviewer(model, budget, forbidden, governance)
				}
			}
			res, err := promoteInstincts(homunculus.NewLayout(), opt, time.Now())
			if err != nil {
				return err
			}
			renderPromote(cmd.OutOrStdout(), res)
			if res.judgeErr != nil {
				// Fail-closed, and said out loud: nothing was promoted, and
				// the reason is not "nothing qualified".
				return fmt.Errorf("instinct promote: nothing was promoted — %w", res.judgeErr)
			}
			if len(res.writeErrs) > 0 {
				return fmt.Errorf("instinct promote: %d write error(s); see output", len(res.writeErrs))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&judge, "judge", true, "run the LLM layer over what the patterns cleared (--judge=false screens with the patterns only and spends no LLM calls)")
	cmd.Flags().StringVar(&model, "model", "", "override the claude model for the judge")
	cmd.Flags().IntVar(&judgeMaxCalls, "judge-max-calls", 0, "cap the judge's LLM calls for this run (default: candidates x votes, capped)")
	cmd.Flags().IntVar(&opt.minProjects, "min-projects", promoteMinProjects, "minimum projects an instinct must appear in")
	cmd.Flags().Float64Var(&opt.minConfidence, "min-confidence", promoteMinConfidence, "minimum mean confidence to promote")
	// Preview by default — promote mutates the shared global corpus, so a
	// bare `bough instinct promote` must not write (ECC prompts [y/N]; bough
	// is non-interactive, so it defaults to dry-run + an explicit --apply).
	cmd.Flags().BoolVar(&opt.dryRun, "dry-run", true, "report what would be promoted without writing (default true)")
	// --apply is the inverse of --dry-run for ergonomics, matching `ecc import`.
	cmd.Flags().BoolFunc("apply", "perform the promotion (= --dry-run=false)", func(string) error {
		opt.dryRun = false
		return nil
	})
	return cmd
}

func renderPromote(w io.Writer, res promoteResult) {
	if len(res.promoted) == 0 && len(res.skippedGlobal) == 0 && res.belowThresh == 0 && len(res.gateHeld) == 0 {
		fmt.Fprintln(w, "(no cross-project instincts found — need an id present in 2+ projects)")
		return
	}
	verb := "promoted"
	if res.dryRun {
		verb = "would promote"
	}
	fmt.Fprintf(w, "%s %d instinct(s) to the global corpus:\n", verb, len(res.promoted))
	for _, c := range res.promoted {
		fmt.Fprintf(w, "  %-44s conf=%.2f  projects=%d\n", truncate(c.id, 44), c.avgConfidence, len(c.entries))
	}
	if len(res.skippedGlobal) > 0 {
		fmt.Fprintf(w, "skipped %d already-global id(s)\n", len(res.skippedGlobal))
	}
	if res.belowThresh > 0 {
		fmt.Fprintf(w, "%d cross-project id(s) below the confidence threshold\n", res.belowThresh)
	}
	// A refusal at the widest blast radius in the system is the last place
	// to hide a decision, so every held id is named with its rule.
	if len(res.gateHeld) > 0 {
		fmt.Fprintf(w, "policy gate withheld %d id(s) from global scope:\n", len(res.gateHeld))
		for _, h := range res.gateHeld {
			fmt.Fprintf(w, "  %-44s %s\n", truncate(h.id, 44), h.rule)
		}
	}
	if res.dryRun {
		fmt.Fprintln(w, "(dry-run: no files written)")
	}
	for _, e := range res.writeErrs {
		fmt.Fprintf(w, "  ! %v\n", e)
	}
}
