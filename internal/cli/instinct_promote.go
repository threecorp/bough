package cli

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
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
	belowThresh   int                // cross-project but under the gate
	writeErrs     []error
	dryRun        bool
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
			res, err := promoteInstincts(homunculus.NewLayout(), opt, time.Now())
			if err != nil {
				return err
			}
			renderPromote(cmd.OutOrStdout(), res)
			if len(res.writeErrs) > 0 {
				return fmt.Errorf("instinct promote: %d write error(s); see output", len(res.writeErrs))
			}
			return nil
		},
	}
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
	if len(res.promoted) == 0 && len(res.skippedGlobal) == 0 && res.belowThresh == 0 {
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
		fmt.Fprintf(w, "%d cross-project id(s) below the promotion gate\n", res.belowThresh)
	}
	if res.dryRun {
		fmt.Fprintln(w, "(dry-run: no files written)")
	}
	for _, e := range res.writeErrs {
		fmt.Fprintf(w, "  ! %v\n", e)
	}
}
