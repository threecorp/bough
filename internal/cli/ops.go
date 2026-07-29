package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/telemetry"
)

// `bough ops` is the one routine an operator runs to see whether the
// continuous-learning loop is working. Before it, answering that meant
// knowing which of doctor / status / instinct status to run and how to
// read each — so in practice nobody ran any of them, and a loop that had
// quietly stopped learning looked exactly like one that had nothing to
// learn.
//
// Two properties matter more than what it prints.
//
// It is READ-ONLY by default. An operator checking on the system must
// never have to wonder whether checking changed it, so the one step that
// writes — the evolve pass — is behind an explicit --generate.
//
// It states what a green run does NOT establish. A routine that says
// "OK" and means "the four things I happen to measure are fine" trains
// the operator to read it as "everything is fine", and the gap between
// those two is where a broken loop lives for weeks.

// opsWindow is how far back the summary counts. It matches the gate's
// history window so the two cannot tell different stories about the
// same log.
func opsWindow() time.Duration { return 14 * 24 * time.Hour }

func newOpsCmd() *cobra.Command {
	var generate bool
	cmd := &cobra.Command{
		Use:   "ops",
		Short: "The continuous-learning operating routine: posture, usage, and what to do next",
		Long: `bough ops reports whether the learning loop is actually working:
the hook/gate posture, what the host has been doing with what bough
produced, and the single next action.

It is READ-ONLY unless --generate is passed. The only step that writes
is the evolve pass, and it is never run implicitly — checking on a
system must not change it.`,
		RunE: func(c *cobra.Command, _ []string) error {
			out := c.OutOrStdout()
			if err := runDoctor(c); err != nil {
				return err
			}
			renderUsageSummary(out, time.Now())
			if !generate {
				fmt.Fprintln(out, "\nread-only run: nothing was written. Pass --generate to run the evolve pass.")
				return nil
			}
			fmt.Fprintln(out, "\n--generate: running the evolve pass (this writes artifacts and spends LLM calls)")
			ev := newEvolveCmd()
			ev.SetOut(out)
			ev.SetErr(c.ErrOrStderr())
			ev.SetArgs([]string{"--generate"})
			return ev.Execute()
		},
	}
	cmd.Flags().BoolVar(&generate, "generate", false, "also run the evolve pass (the only step that writes)")
	return cmd
}

// renderUsageSummary prints what the host DID with what bough produced.
// Every number is derived through the telemetry package's single decode
// path, so this view and the completion gate cannot disagree about the
// same log.
func renderUsageSummary(w io.Writer, now time.Time) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage (what the host did with what bough produced):")

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(w, "    • cannot resolve the working directory")
		return
	}
	ident, err := homunculus.DetectIdentity(resolveMonorepoRoot(cwd))
	if err != nil {
		fmt.Fprintln(w, "    • no project identity here — run this inside a bough project")
		return
	}
	path := homunculus.NewLayout().TelemetryFile(ident.ID)
	log, lerr := telemetry.Load(path)
	if lerr != nil {
		fmt.Fprintf(w, "    ✗ telemetry unreadable: %v\n", lerr)
		return
	}
	if len(log.Events) == 0 {
		fmt.Fprintf(w, "    • nothing recorded yet (%s)\n", path)
		fmt.Fprintln(w, "      This is the expected state before the hooks have seen a session.")
		return
	}

	win := log.Window(now.Add(-opsWindow()), now)
	pulls := telemetry.PullsBySlug(win)

	// The drift row goes FIRST when it fires. Every number below it is a
	// zero that means "I could not tell", and printing those first is how
	// a broken parser reads as a finding.
	if rows := log.Drift(); len(rows) > 0 {
		fmt.Fprintf(w, "    ✗ SCHEMA DRIFT — the reader could not make sense of %d thing(s) in the log:\n", len(rows))
		for _, r := range rows {
			fmt.Fprintf(w, "        - %s\n", r)
		}
		fmt.Fprintln(w, "      Fix this before reading the numbers below: a count the reader cannot")
		fmt.Fprintln(w, "      parse looks exactly like a count of zero.")
	}

	fmt.Fprintf(w, "    • skills pulled: %d pull(s) across %d skill(s) in the last %s%s\n",
		totalOf(pulls), len(pulls), roundDaysDur(opsWindow()), topSlugs(pulls))
	fmt.Fprintf(w, "    • retrieval breadth: %d distinct instinct(s) injected in the last %s\n",
		telemetry.DistinctIDs(win), roundDaysDur(opsWindow()))

	unjudged := telemetry.UnjudgedPromotions(win)
	marker := "•"
	if unjudged > 0 {
		marker = "✗"
	}
	fmt.Fprintf(w, "    %s promoted without being judged: %d candidate(s) in the last %s\n",
		marker, unjudged, roundDaysDur(opsWindow()))
	if unjudged > 0 {
		fmt.Fprintln(w, "      The gate fails open on purpose, so this is knowledge that reached the")
		fmt.Fprintln(w, "      corpus unscreened. Re-run the observer to judge what is still staged.")
	}

	if oldest, ok := log.Oldest(); ok {
		fmt.Fprintf(w, "    • history: %s, from %s\n", roundDaysDur(now.Sub(oldest)), path)
	}
	fmt.Fprintln(w, "\n    A green run here establishes that the hooks fired and the numbers parse.")
	fmt.Fprintln(w, "    It does NOT establish that the instincts are correct, that the skills say")
	fmt.Fprintln(w, "    anything useful, or that anything was learned from the last session.")
}

func totalOf(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// topSlugs names the most-pulled skills, and says so when it cuts the
// list — a bounded list that reads as the whole set is the silent-cap
// failure this project's own rules forbid.
func topSlugs(pulls map[string]int) string {
	if len(pulls) == 0 {
		return ""
	}
	type kv struct {
		slug string
		n    int
	}
	all := make([]kv, 0, len(pulls))
	for s, n := range pulls {
		all = append(all, kv{s, n})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].slug < all[j].slug
	})
	const show = 3
	names := ""
	for i, e := range all {
		if i == show {
			return fmt.Sprintf("%s — showing %d of %d)", names, show, len(all))
		}
		if i == 0 {
			names = fmt.Sprintf(" (%s×%d", e.slug, e.n)
			continue
		}
		names += fmt.Sprintf(", %s×%d", e.slug, e.n)
	}
	return names + ")"
}

func roundDaysDur(d time.Duration) string {
	days := d.Hours() / 24
	if days < 1 {
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%.0fd", days)
}
