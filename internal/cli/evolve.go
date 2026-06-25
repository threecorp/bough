package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/evolve"
	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/prompts"
	"github.com/ikeikeikeike/bough/internal/provider/claudecli"
)

// newEvolveCmd wires `bough evolve` — the 5-gate clustering pipeline
// that turns the accumulated instinct corpus into skills / agents /
// commands. Default is preview (= no LLM call, no writes); --generate
// runs GATE 5 (claude --print) on the gate-passing clusters and
// writes the artifacts. The split mirrors ECC's
// `/evolve-skill-manual-v3` vs `--generate` UX.
func newEvolveCmd() *cobra.Command {
	var (
		root        string
		generate    bool
		judge       string
		model       string
		noSymlink   bool
		maxCalls    int
	)
	cmd := &cobra.Command{
		Use:   "evolve",
		Short: "Cluster instincts into skills / agents / commands (5-gate pipeline)",
		Long: `bough evolve runs the ECC v3 five-gate clustering pipeline over
the per-project instinct corpus:

  GATE 1 member count → GATE 2 cohesion → GATE 3' lexicon coverage →
  GATE 4 relative isolation → GATE 5 LLM semantic judge

Default is preview: GATE 1-4 run mechanically, surviving clusters
are listed, and NO claude --print call is made. Pass --generate to
run GATE 5 (= one claude --print per gate-passing cluster, inside
your Claude Code subscription) and write evolved/skills/<slug>/
SKILL.md (+ ~/.claude/skills symlink), evolved/agents/<slug>.md,
evolved/commands/<slug>.md.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			cwd := root
			if cwd == "" {
				w, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("evolve: getwd: %w", err)
				}
				cwd = w
			}
			ident, err := homunculus.DetectIdentity(cwd)
			if err != nil {
				return err
			}
			layout := homunculus.NewLayout()
			instincts, _ := homunculus.ScanInstincts(layout.InstinctsDir(ident.ID))
			stdout := cmd.OutOrStdout()
			if len(instincts) == 0 {
				fmt.Fprintf(stdout, "no instincts under %s — run `bough observer run-once` first\n", layout.InstinctsDir(ident.ID))
				return nil
			}

			labelsPath := layout.ClusterLabels(ident.ID)
			labels, err := evolve.LoadLabels(labelsPath)
			if err != nil {
				return err
			}

			th := evolve.DefaultThresholds()

			// Preview path: GATE 1-4 only, no judge call.
			if !generate {
				return runEvolvePreview(stdout, ident, instincts, labels, th)
			}

			// Generate path: wire GATE 5 through claudecli.
			j, prov, err := buildEvolveJudge(judge, model, maxCalls)
			if err != nil {
				return err
			}
			pipe := evolve.Pipeline{
				Judge:      j,
				Thresholds: th,
				Now:        time.Now,
			}
			out, err := pipe.Run(ctx, instincts, labels)
			if err != nil {
				return err
			}
			return persistEvolveOutcome(stdout, cmd.ErrOrStderr(), ident, layout, labels, labelsPath, out, th, noSymlink, prov)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().BoolVar(&generate, "generate", false, "run GATE 5 (claude --print) + write artifacts (default: preview only)")
	cmd.Flags().StringVar(&judge, "judge", "claude", "GATE 5 backend: claude (= claude --print)")
	cmd.Flags().StringVar(&model, "model", "", "override the claude model for GATE 5 (default: provider default)")
	cmd.Flags().BoolVar(&noSymlink, "no-symlink", false, "do not create ~/.claude/skills symlinks for generated skills")
	cmd.Flags().IntVar(&maxCalls, "max-calls", 0, "override the per-session GATE 5 call cap")
	return cmd
}

func runEvolvePreview(w io.Writer, ident homunculus.ProjectIdentity, instincts []*homunculus.Instinct, labels *evolve.ClusterLabels, th evolve.Thresholds) error {
	priors := labels.Priors()
	priorUnion := labels.PriorUnion()
	clusters := evolve.Discover(instincts, priors, th)

	fmt.Fprintf(w, "project: %s (%s)\n", ident.Name, ident.ID)
	fmt.Fprintf(w, "instincts: %d   clusters: %d   priors: %d\n\n", len(instincts), len(clusters), len(priors))

	passed := 0
	for _, c := range clusters {
		gate := evolve.EvaluateGatesWithPriorUnion(c, priorUnion, th)
		if gate.Passed {
			passed++
			fmt.Fprintf(w, "  PASS gates  %d members  coh=%.2f cov=%.2f iso=%.2f\n",
				gate.MemberCount, gate.Cohesion, gate.LexiconCoverage, gate.RelIsolation)
			for _, m := range c.Members {
				fmt.Fprintf(w, "       - %s\n", m.ID)
			}
		}
	}
	fmt.Fprintf(w, "\n%d clusters passed the mechanical gates (eligible for GATE 5).\n", passed)
	fmt.Fprintf(w, "Run `bough evolve --generate` to judge + emit (= %d claude --print calls max).\n", passed)
	return nil
}

func persistEvolveOutcome(stdout, stderr io.Writer, ident homunculus.ProjectIdentity, layout homunculus.Layout, labels *evolve.ClusterLabels, labelsPath string, out evolve.Outcome, th evolve.Thresholds, noSymlink bool, _ *claudecli.Provider) error {
	now := time.Now()
	symlinkDir := ""
	if !noSymlink {
		if home, err := os.UserHomeDir(); err == nil {
			symlinkDir = filepath.Join(home, ".claude", "skills")
		}
	}
	skillsDir := layout.EvolvedSkillsDir(ident.ID)
	agentsDir := layout.EvolvedAgentsDir(ident.ID)
	commandsDir := layout.EvolvedCommandsDir(ident.ID)

	skillsWritten := 0
	for _, s := range out.Skills {
		art := evolve.RenderSkill(s.Label, s.Description, s.Cluster, th, now)
		if _, err := evolve.WriteSkill(skillsDir, symlinkDir, art); err != nil {
			fmt.Fprintf(stderr, "  skill %s: %v\n", s.Label, err)
			continue
		}
		if s.NewLabel {
			labels.Add(s.Label, s.Description)
		}
		skillsWritten++
		fmt.Fprintf(stdout, "  skill   %s (%s, %d members)\n", s.Label, s.Verdict.Decision, len(s.Cluster.Members))
	}

	agentsWritten := 0
	for _, a := range out.Agents {
		art := evolve.RenderAgent(a.Label, a.Cluster, now)
		if _, err := evolve.WriteAgent(agentsDir, art); err != nil {
			fmt.Fprintf(stderr, "  agent %s: %v\n", a.Label, err)
			continue
		}
		agentsWritten++
		fmt.Fprintf(stdout, "  agent   %s (%d members)\n", a.Label, len(a.Cluster.Members))
	}

	commandsWritten := 0
	for _, c := range out.Commands {
		art := evolve.RenderCommand(c.Instinct, now)
		if _, err := evolve.WriteCommand(commandsDir, art); err != nil {
			fmt.Fprintf(stderr, "  command %s: %v\n", art.Slug, err)
			continue
		}
		commandsWritten++
		fmt.Fprintf(stdout, "  command %s\n", art.Slug)
	}

	if err := labels.Save(labelsPath, now); err != nil {
		return fmt.Errorf("evolve: save cluster-labels: %w", err)
	}

	fmt.Fprintf(stdout, "\nwrote skills=%d agents=%d commands=%d (rejected clusters=%d)\n",
		skillsWritten, agentsWritten, commandsWritten, len(out.Rejected))
	return nil
}

// buildEvolveJudge wires the GATE 5 backend. Only "claude" is
// supported in v0.9.1; the flag exists so v0.9.x can add a replay /
// heuristic backend without a breaking change.
func buildEvolveJudge(backend, model string, maxCalls int) (evolve.Judge, *claudecli.Provider, error) {
	switch backend {
	case "", "claude":
	default:
		return nil, nil, fmt.Errorf("evolve: unknown --judge %q (only 'claude' in v0.9.1)", backend)
	}
	resolver := prompts.NewResolver()
	tpl, err := resolver.Get(prompts.TemplateJudge)
	if err != nil {
		return nil, nil, err
	}
	prov := claudecli.NewProvider()
	if model != "" {
		prov.Model = model
	}
	if maxCalls > 0 {
		prov.Limiter.MaxCallsPerSession = maxCalls
	}
	render := func(data evolve.JudgeData) (string, string, error) {
		body, err := renderJudgeTemplate(tpl.Body, data)
		return body, tpl.Version, err
	}
	generate := func(ctx context.Context, promptBody string) ([]byte, error) {
		raw, _, err := prov.GenerateRaw(ctx, promptBody)
		return raw, err
	}
	return evolve.NewClaudeJudge(render, generate), prov, nil
}

func renderJudgeTemplate(body string, data evolve.JudgeData) (string, error) {
	tpl, err := template.New("judge").Parse(body)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := tpl.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}
