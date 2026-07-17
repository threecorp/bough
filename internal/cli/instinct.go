package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// newInstinctCmd wires `bough instinct` — the read-side counterpart
// to `bough observer run-once`. v0.9.0 ships status / list / show so
// an operator can see what the observer wrote without grepping the
// homunculus tree directly. v0.9.13 adds promote (project → global
// corpus, ECC auto-promotion parity); mint stays out because the
// canonical mint path is via `bough observer run-once`.
// newInstinctCmd is the namespace for bough's continuous-learning surface —
// everything that fills, reads, or spends the instinct corpus.
//
// The grouping is not invented here: `.bough.yaml` already nests this domain
// under one `instinct:` key, down to `instinct.observer.autostart`. Only the
// CLI disagreed, spreading `observer` / `evolve` / `ecc` across the root next
// to `create` and `remove` as if they were peers of the worktree lifecycle.
// Mirroring the config the operator already writes means the two vocabularies
// cannot drift, and it costs the root four entries.
//
// The reading verbs keep their spelling (`bough instinct list` was already
// right); only the three that lived at root moved under it.
func newInstinctCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "instinct",
		Short: "The continuous-learning corpus: observe, inspect, evolve, import",
		Long: `bough instinct is the whole continuous-learning surface, matching
the instinct: block in .bough.yaml that configures it.

  observer   mint instincts from what the hooks recorded (opt-in;
             each pass is a claude --print on your subscription)
  status     per-project totals + confidence histogram
  list/show  audit the corpus without digging into
             ~/.local/share/bough-homunculus/projects/<hash>/
  promote    lift cross-project instincts into the global corpus
  evolve     cluster the corpus into skills / agents / commands
  import     migrate an existing everything-claude-code corpus in

Nothing here runs unless instinct.enabled is true in .bough.yaml.`,
	}
	cmd.AddCommand(
		// Reading the corpus.
		newInstinctStatusCmd(), newInstinctListCmd(), newInstinctShowCmd(), newInstinctPromoteCmd(),
		// Filling it, spending it, seeding it from elsewhere.
		newObserverCmd(), newEvolveCmd(), newEccImportCmd(),
	)
	return cmd
}

func newInstinctStatusCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Print per-project instinct totals + confidence histogram",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ident, layout, instincts, soft, err := loadInstinctsForCWD(cmd.Context(), root)
			if err != nil {
				return err
			}
			renderStatus(cmd.OutOrStdout(), ident, layout, instincts, soft)
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	return cmd
}

func newInstinctListCmd() *cobra.Command {
	var (
		root    string
		sortBy  string
		limit   int
		domain  string
		minConf float64
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List instincts (id, trigger, confidence, domain)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _, instincts, _, err := loadInstinctsForCWD(cmd.Context(), root)
			if err != nil {
				return err
			}
			filtered := filterInstincts(instincts, domain, minConf)
			sortInstincts(filtered, sortBy)
			if limit > 0 && len(filtered) > limit {
				filtered = filtered[:limit]
			}
			renderList(cmd.OutOrStdout(), filtered)
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().StringVar(&sortBy, "sort", "confidence", "sort key: confidence | id | recent")
	cmd.Flags().IntVar(&limit, "limit", 0, "limit rows (0 = no cap)")
	cmd.Flags().StringVar(&domain, "domain", "", "filter by domain (e.g. workflow, testing)")
	cmd.Flags().Float64Var(&minConf, "min-confidence", 0, "filter rows with confidence below this value")
	return cmd
}

func newInstinctShowCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "show <id>",
		Args:  cobra.ExactArgs(1),
		Short: "Print one instinct file verbatim by id",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			ident, layout, instincts, _, err := loadInstinctsForCWD(cmd.Context(), root)
			if err != nil {
				return err
			}
			for _, in := range instincts {
				if in.ID == id {
					raw, rerr := os.ReadFile(in.Path)
					if rerr != nil {
						return fmt.Errorf("instinct show: read %s: %w", in.Path, rerr)
					}
					_, _ = cmd.OutOrStdout().Write(raw)
					return nil
				}
			}
			return fmt.Errorf("instinct %q not found under %s (project=%s)", id, layout.InstinctsDir(ident.ID), ident.Name)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	return cmd
}

// loadInstinctsForCWD is the shared resolution + scan path the
// three subcommands use. Returns the ProjectIdentity (for the
// header line), the Layout (for the path under error messages),
// the parsed instincts, and any soft scan errors.
func loadInstinctsForCWD(ctx context.Context, root string) (homunculus.ProjectIdentity, homunculus.Layout, []*homunculus.Instinct, []error, error) {
	_ = ctx
	cwd := root
	if cwd == "" {
		w, err := os.Getwd()
		if err != nil {
			return homunculus.ProjectIdentity{}, homunculus.Layout{}, nil, nil, fmt.Errorf("instinct: getwd: %w", err)
		}
		cwd = w
	}
	ident, err := homunculus.DetectIdentity(cwd)
	if err != nil {
		return homunculus.ProjectIdentity{}, homunculus.Layout{}, nil, nil, err
	}
	layout := homunculus.NewLayout()
	instincts, soft := homunculus.ScanInstincts(layout.InstinctsDir(ident.ID))
	return ident, layout, instincts, soft, nil
}

func renderStatus(w io.Writer, ident homunculus.ProjectIdentity, layout homunculus.Layout, instincts []*homunculus.Instinct, soft []error) {
	fmt.Fprintf(w, "project: %s (%s)\n", ident.Name, ident.ID)
	fmt.Fprintf(w, "root:    %s\n", ident.Root)
	fmt.Fprintf(w, "remote:  %s\n", emptyIfBlank(ident.Remote))
	fmt.Fprintf(w, "store:   %s\n", layout.InstinctsDir(ident.ID))
	fmt.Fprintf(w, "count:   %d\n", len(instincts))
	// Surface the files ScanInstincts skipped (unreadable, or filename ≠
	// frontmatter id) so the operator can reconcile a count that is lower
	// than the .md file count instead of wondering where they went.
	if len(soft) > 0 {
		fmt.Fprintf(w, "skipped: %d (unreadable / filename↔frontmatter-id mismatch)\n", len(soft))
	}
	if len(instincts) == 0 {
		fmt.Fprintln(w, "(no instincts yet — run `bough observer run-once` after the operator records some Claude Code sessions)")
		return
	}
	// Confidence histogram in 5 buckets.
	buckets := []float64{0.4, 0.55, 0.70, 0.80, 0.90}
	labels := []string{"<0.40", "0.40-0.55", "0.55-0.70", "0.70-0.80", ">=0.80"}
	hist := make([]int, len(buckets))
	for _, in := range instincts {
		idx := len(buckets) - 1
		for i, b := range buckets {
			if in.Confidence < b {
				idx = i
				break
			}
		}
		hist[idx]++
	}
	fmt.Fprintln(w, "confidence histogram:")
	for i, label := range labels {
		fmt.Fprintf(w, "  %-10s %4d %s\n", label, hist[i], strings.Repeat("█", hist[i]))
	}

	// Top-3 most recent
	recent := append([]*homunculus.Instinct(nil), instincts...)
	sort.SliceStable(recent, func(i, j int) bool {
		return recent[i].LastSeen.After(recent[j].LastSeen)
	})
	cutoff := 3
	if len(recent) < cutoff {
		cutoff = len(recent)
	}
	fmt.Fprintln(w, "most recent:")
	for _, in := range recent[:cutoff] {
		fmt.Fprintf(w, "  %s  (conf=%.2f, %s)\n", in.ID, in.Confidence, lastSeen(in))
	}
}

func renderList(w io.Writer, instincts []*homunculus.Instinct) {
	if len(instincts) == 0 {
		fmt.Fprintln(w, "(no instincts matched the filter)")
		return
	}
	fmt.Fprintf(w, "%-44s %-5s %-13s %s\n", "ID", "CONF", "DOMAIN", "TRIGGER")
	for _, in := range instincts {
		fmt.Fprintf(w, "%-44s %.2f  %-13s %s\n",
			truncate(in.ID, 44),
			in.Confidence,
			truncate(in.Domain, 13),
			truncate(in.Trigger, 90))
	}
}

func filterInstincts(in []*homunculus.Instinct, domain string, minConf float64) []*homunculus.Instinct {
	if domain == "" && minConf <= 0 {
		return in
	}
	out := make([]*homunculus.Instinct, 0, len(in))
	for _, row := range in {
		if domain != "" && !strings.EqualFold(row.Domain, domain) {
			continue
		}
		if row.Confidence < minConf {
			continue
		}
		out = append(out, row)
	}
	return out
}

func sortInstincts(rows []*homunculus.Instinct, key string) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "id", "":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	case "confidence":
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].Confidence == rows[j].Confidence {
				return rows[i].ID < rows[j].ID
			}
			return rows[i].Confidence > rows[j].Confidence
		})
	case "recent":
		sort.SliceStable(rows, func(i, j int) bool {
			return rows[i].LastSeen.After(rows[j].LastSeen)
		})
	}
}

func truncate(s string, max int) string {
	if max <= 1 || len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func emptyIfBlank(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(not detected)"
	}
	return s
}

func lastSeen(in *homunculus.Instinct) string {
	if in.LastSeen.IsZero() {
		return "no last_seen"
	}
	d := time.Since(in.LastSeen)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hr ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
