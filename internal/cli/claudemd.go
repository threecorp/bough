package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// CLAUDE.md proposal gates. ECC continuous-learning-v2's
// evolve-claudemd.sh proposes an ADD when an instinct's confidence
// reaches >= 0.9 and a REMOVE when it has decayed to <= 0.3 and is more
// than 30 days old. bough's confidence ladder (internal/session) caps at
// 0.85, so a verbatim 0.9 ADD gate would make the feature inert — never
// firing. The ADD gate is therefore calibrated to bough's 0.30-0.85
// scale: 0.80 is the top two bands, the bough analogue of ECC's near-max
// "high confidence", set below the 0.85 cap so real instincts qualify.
// The REMOVE gate (0.30 floor + 30 days) maps to bough's bottom band
// directly and is kept verbatim. This is the deliberate per-item
// adaptation, not a blind copy.
const (
	claudemdAddConfidence    = 0.80
	claudemdRemoveConfidence = 0.30
	claudemdRemoveAgeDays    = 30
	claudemdProposalsRelName = "claudemd-proposals.md"
)

// claudemdProposals holds the two proposal buckets for one project.
type claudemdProposals struct {
	add    []*homunculus.Instinct
	remove []*homunculus.Instinct
}

func (p claudemdProposals) empty() bool { return len(p.add) == 0 && len(p.remove) == 0 }

// collectClaudemdProposals partitions a project's instincts into ADD
// (high confidence) and REMOVE (low confidence + aged) proposals. It is
// pure + deterministic (no LLM): the instinct trigger/body already reads
// as a candidate CLAUDE.md rule, so ECC needs no model to phrase it and
// neither does bough.
func collectClaudemdProposals(instincts []*homunculus.Instinct, now time.Time) claudemdProposals {
	var p claudemdProposals
	cutoff := now.AddDate(0, 0, -claudemdRemoveAgeDays)
	for _, in := range instincts {
		switch {
		case in.Confidence >= claudemdAddConfidence:
			p.add = append(p.add, in)
		case in.Confidence <= claudemdRemoveConfidence && !in.FirstSeen.IsZero() && in.FirstSeen.Before(cutoff):
			p.remove = append(p.remove, in)
		}
	}
	sort.SliceStable(p.add, func(i, j int) bool { return p.add[i].Confidence > p.add[j].Confidence })
	sort.SliceStable(p.remove, func(i, j int) bool { return p.remove[i].Confidence < p.remove[j].Confidence })
	return p
}

// renderClaudemdProposals renders the proposals as the markdown document
// bough writes to .claude/claudemd-proposals.md for human review,
// mirroring ECC evolve-claudemd.sh's section layout.
func renderClaudemdProposals(p claudemdProposals, now time.Time) string {
	var b strings.Builder
	b.WriteString("# CLAUDE.md Evolution Proposals\n")
	fmt.Fprintf(&b, "Generated: %s\n\n", now.UTC().Format(time.RFC3339))

	if len(p.add) > 0 {
		b.WriteString("## Proposed Additions (high-confidence instincts)\n\n")
		for _, in := range p.add {
			fmt.Fprintf(&b, "### ADD — %s\n", in.ID)
			fmt.Fprintf(&b, "- confidence: %.2f\n", in.Confidence)
			if in.Domain != "" {
				fmt.Fprintf(&b, "- domain: %s\n", in.Domain)
			}
			if in.Trigger != "" {
				fmt.Fprintf(&b, "- trigger: %s\n", in.Trigger)
			}
			if rule := firstActionLine(in.Body); rule != "" {
				fmt.Fprintf(&b, "- rule: %s\n", rule)
			}
			b.WriteString("\n")
		}
	}

	if len(p.remove) > 0 {
		b.WriteString("## Proposed Removals (low-confidence, aged)\n\n")
		for _, in := range p.remove {
			fmt.Fprintf(&b, "### REMOVE — %s\n", in.ID)
			fmt.Fprintf(&b, "- confidence: %.2f\n", in.Confidence)
			if !in.FirstSeen.IsZero() {
				fmt.Fprintf(&b, "- first_seen: %s\n", in.FirstSeen.UTC().Format("2006-01-02"))
			}
			fmt.Fprintf(&b, "- reason: confidence <= %.2f and older than %d days\n", claudemdRemoveConfidence, claudemdRemoveAgeDays)
			b.WriteString("\n")
		}
	}

	b.WriteString("---\n")
	b.WriteString("Review these and hand-apply to CLAUDE.md. bough never edits CLAUDE.md automatically.\n")
	return b.String()
}

// firstActionLine extracts a one-line rule from an instinct body: the
// first non-empty line under a "## Action" heading, else the first
// non-empty non-heading line.
func firstActionLine(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(l)), "## action") {
			for _, n := range lines[i+1:] {
				if s := strings.TrimSpace(n); s != "" {
					return s
				}
			}
		}
	}
	for _, l := range lines {
		s := strings.TrimSpace(l)
		if s != "" && !strings.HasPrefix(s, "#") {
			return s
		}
	}
	return ""
}

// runEvolveClaudeMD is the shared body: scan the project's instincts,
// partition into ADD / REMOVE proposals, and either preview the markdown
// to stdout (default) or write it to the proposals file (--write). Pure
// filesystem — no claude --print call, and CLAUDE.md is never edited
// automatically; the operator reviews the proposals and applies by hand.
func runEvolveClaudeMD(out io.Writer, root, outPath string, write bool, now time.Time) error {
	cwd := root
	if cwd == "" {
		w, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("session-evolve-claudemd: getwd: %w", err)
		}
		cwd = w
	}
	monorepoRoot := resolveMonorepoRoot(cwd)
	ident, err := homunculus.DetectIdentity(monorepoRoot)
	if err != nil {
		return nil // non-git / unresolvable → clean no-op
	}
	layout := homunculus.NewLayout()
	instincts, _ := homunculus.ScanInstincts(layout.InstinctsDir(ident.ID))
	prop := collectClaudemdProposals(instincts, now)
	if prop.empty() {
		fmt.Fprintln(out, "(no CLAUDE.md proposals — no instinct crossed the add/remove gates)")
		if write {
			// Clear a stale proposals file from a prior session: with nothing
			// crossing the gates now, leaving the old file would have the
			// operator review proposals that no longer reflect current
			// instinct confidence. Missing file is fine.
			target := claudemdTargetPath(outPath, monorepoRoot)
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("session-evolve-claudemd: clear stale %s: %w", target, err)
			}
		}
		return nil
	}
	doc := renderClaudemdProposals(prop, now)
	if !write {
		fmt.Fprint(out, doc)
		fmt.Fprintf(out, "\n(preview: %d addition(s) + %d removal(s); pass --write to save the proposals file)\n", len(prop.add), len(prop.remove))
		return nil
	}
	target := claudemdTargetPath(outPath, monorepoRoot)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("session-evolve-claudemd: mkdir %s: %w", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte(doc), 0o644); err != nil {
		return fmt.Errorf("session-evolve-claudemd: write %s: %w", target, err)
	}
	fmt.Fprintf(out, "wrote %d addition(s) + %d removal(s) to %s\n", len(prop.add), len(prop.remove), target)
	return nil
}

// claudemdTargetPath resolves where the proposals file is written: the
// explicit --out override, else <monorepoRoot>/.claude/<proposals-name>.
func claudemdTargetPath(outPath, monorepoRoot string) string {
	if outPath != "" {
		return outPath
	}
	return filepath.Join(monorepoRoot, ".claude", claudemdProposalsRelName)
}

// newSessionEvolveClaudeMDCmd wires `bough session-evolve-claudemd` —
// the CLAUDE.md-evolution proposer, mirroring ECC evolve-claudemd.sh.
// Pure filesystem (no claude --print): it scans the project's instincts
// and proposes high-confidence ones as CLAUDE.md additions + decayed
// ones as removals, for human review. Preview by default; --write saves
// the proposals file. CLAUDE.md itself is never edited automatically.
func newSessionEvolveClaudeMDCmd() *cobra.Command {
	var (
		root  string
		out   string
		write bool
	)
	cmd := &cobra.Command{
		Use:   "session-evolve-claudemd",
		Short: "Propose CLAUDE.md additions/removals from instinct confidence (no LLM)",
		Long: `bough session-evolve-claudemd mirrors ECC continuous-learning-v2's
evolve-claudemd.sh: it scans the project's instincts and proposes the
high-confidence ones as CLAUDE.md additions and the decayed, aged ones as
removals, writing .claude/claudemd-proposals.md for human review. Pure
filesystem — no claude --print call, and CLAUDE.md is never edited
automatically. Preview to stdout by default; pass --write to save the
proposals file.

Confidence gates are calibrated to bough's 0.30-0.85 ladder: ECC uses
>=0.9 of its 0.1-0.95 scale, but bough's confidence caps at 0.85, so ADD
fires at >=0.80 (the top two bands, set below the cap so it is not inert)
and REMOVE at <=0.30 aged >30d.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runEvolveClaudeMD(cmd.OutOrStdout(), root, out, write, time.Now())
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().StringVar(&out, "out", "", "proposals file path (default: <root>/.claude/claudemd-proposals.md)")
	cmd.Flags().BoolVar(&write, "write", false, "write the proposals file (default: preview to stdout)")
	return cmd
}
