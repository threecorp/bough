package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/evolve"
	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/inject"
	"github.com/ikeikeikeike/bough/internal/retrieve"
)

// runInjectContext resolves the current project's instinct pools and
// writes the confidence-ranked injection block to out. Shared by
// dispatchInjectContext (hook.go's UserPromptSubmit hook path) and
// newInjectContextCmd's RunE (the manual preview command below) so
// the two paths cannot drift apart — before this helper existed they
// already needed an identical fix hand-applied twice (switching
// DetectIdentity(cwd) to DetectIdentity(resolveMonorepoRoot(cwd)) in
// both places).
func runInjectContext(out io.Writer, root string, opts inject.Options) error {
	cwd := root
	if cwd == "" {
		w, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("inject-context: getwd: %w", err)
		}
		cwd = w
	}
	// Resolve identity from the MONOREPO ROOT, not the raw cwd: the
	// observation writer (resolveHomunculusObsPath), the observer
	// daemon that mints the instinct files, session-end, and preserve
	// all pool to DetectIdentity(resolveMonorepoRoot(cwd)). In a
	// multi-repo monorepo / worktree (the .bough.yaml root is not a
	// git repo; each sub-repo has its own origin) the raw-cwd id would
	// differ from the writer's id, so this injector would read an
	// empty project and surface nothing — the loop's whole payoff.
	monoRoot := resolveMonorepoRoot(cwd)
	ident, err := homunculus.DetectIdentity(monoRoot)
	if err != nil {
		// A non-git directory is not an error for a hook — just
		// emit nothing so the prompt is unaffected.
		return nil
	}
	layout := homunculus.NewLayout()
	project, _ := homunculus.ScanInstincts(layout.InstinctsDir(ident.ID))
	global, _ := homunculus.ScanInstincts(layout.GlobalInstinctsDir())
	// Where the session is working is part of the query. Derived
	// relative to the monorepo root, so the repo name itself — which
	// matches a large share of the corpus and would drown short prompts
	// — contributes nothing.
	if len(opts.ContextTokens) == 0 {
		opts.ContextTokens = retrieve.ContextTokens(monoRoot, cwd)
	}
	// Suppressing skill-covered instincts is gated on evidence, not on a
	// config flag alone: an operator who turns it on before the pull path
	// works loses the knowledge from both paths at once. The readiness
	// check is the authority, so a premature `exclude_skill_covered: true`
	// cannot take effect.
	if opts.ExcludeIDs == nil {
		opts.ExcludeIDs = skillCoveredExclusions(monoRoot, ident.ID, layout)
	}
	block, n := inject.Build(project, global, opts)
	// Human-authored corrections outrank minted instincts and are not
	// scored, so they are prepended rather than merged into the ranking
	// — and they are emitted even when nothing cleared the confidence
	// floor, since ground truth does not depend on the corpus having
	// anything to say.
	// Both the config lookup and the file lookup anchor on the SAME
	// monorepo root the identity resolved from. Passing the raw `root`
	// parameter here would read a different (possibly empty) directory,
	// so an operator's configured path would be silently ignored when the
	// hook fires from a sub-repo.
	lessons := inject.LessonsBlock(monoRoot, lessonsPaths(monoRoot), opts.MaxBytes)
	if lessons == "" && n == 0 {
		return nil // nothing to say → clean no-op
	}
	fmt.Fprint(out, lessons)
	if n > 0 {
		fmt.Fprint(out, block)
	}
	return nil
}

// lessonsPaths reads the operator's lessons-file locations from
// .bough.yaml, falling back to the conventional set. A missing or
// unreadable config is not an error: the hook fires on every prompt, so
// it degrades to the conventions rather than failing the turn.
func lessonsPaths(root string) []string {
	cfg, err := loadConfigQuiet(resolveConfigPath(&cobra.Command{}, root))
	if err != nil {
		return nil
	}
	return cfg.Instinct.Lessons.Paths
}

// newInjectContextCmd wires `bough inject-context` — the
// UserPromptSubmit hook handler. Claude Code calls it on every user
// prompt; whatever it writes to stdout is folded into the next turn's
// context (= billed as input tokens at the operator's subscription
// rate). The handler selects the highest-confidence instincts for the
// current project + global scope, caps the block at ~9.5 KB, and
// prints it. No LLM call — the hook fires on every keystroke-to-
// response cycle, so it stays pure filesystem.
//
// Wired into .claude/settings.json via `bough hook install`; can also
// be run by hand to preview the block an operator's next prompt would
// receive.
func newInjectContextCmd() *cobra.Command {
	var (
		root     string
		maxBytes int
		maxN     int
		minConf  float64
		prompt   string
	)
	cmd := &cobra.Command{
		Use:   "inject-context",
		Short: "Print the confidence-ranked instinct block for the UserPromptSubmit hook",
		Long: `bough inject-context is the UserPromptSubmit hook handler. It
prints the instincts most relevant to the prompt, for the current
project (+ global scope), so Claude Code folds them into the next
turn's context.

Ranking is by relevance to --prompt, which the hook always passes:
three channels (exact identifier/path hits, BM25 over the corpus, and
recency) are fused, and a candidate no lexical channel found is
dropped even when it is recent — so an off-topic prompt correctly gets
nothing. Confidence is audit data, not a ranking key. Run by hand
WITHOUT --prompt and there is nothing to rank against, so it falls
back to confidence order.

The block is byte-capped (default ~9.5 KB) because the stdout is
billed as input tokens. No claude --print call is made — selection is
pure filesystem.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := inject.Options{
				MaxBytes:     maxBytes,
				MaxInstincts: maxN,
				Prompt:       prompt,
			}
			// Only override MinConfidence when the operator actually
			// passed the flag: --min-confidence 0 is a legitimate "no
			// floor" request, and inject.Options.MinConfidence must see
			// that as an explicit 0.0, not as "unset" (which would
			// silently substitute the 0.50 default and drop every
			// instinct in the real, reachable 0.30-0.49 band).
			if cmd.Flags().Changed("min-confidence") {
				opts.MinConfidence = &minConf
			}
			return runInjectContext(cmd.OutOrStdout(), root, opts)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().IntVar(&maxBytes, "max-bytes", 0, "byte cap on the injected block (default 9500)")
	cmd.Flags().IntVar(&maxN, "max-instincts", 0, "max instincts to consider (default 40)")
	cmd.Flags().Float64Var(&minConf, "min-confidence", 0, "drop instincts below this confidence (default 0.50)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "rank against this prompt (the hook passes the real one; empty falls back to confidence order)")
	return cmd
}

// skillCoveredExclusions returns the instinct ids to drop from the
// pushed block — but ONLY when the readiness gate says the pull path can
// carry them. The gate is the authority, not the config flag: an
// operator who sets `exclude_skill_covered: true` before the portfolio
// is deployed would lose that knowledge from both paths at once, and the
// symptom (the loop goes quiet) does not point at the cause.
//
// Returns nil in every uncertain case. Pushing knowledge that is also
// pullable costs prompt budget; NOT pushing knowledge that turns out not
// to be pullable costs the knowledge.
func skillCoveredExclusions(monoRoot, projectID string, layout homunculus.Layout) map[string]struct{} {
	cfg, err := loadConfigQuiet(resolveConfigPath(&cobra.Command{}, monoRoot))
	if err != nil || !cfg.Instinct.ExcludeSkillCovered {
		return nil
	}
	skillsDir := layout.EvolvedSkillsDir(projectID)
	deployedDir := filepath.Join(monoRoot, ".claude", "skills")
	coveragePath := layout.SkillCoverageFile(projectID)
	// The gate reads the coverage registry to judge it, so take the copy
	// it already parsed rather than reading the same file a second time
	// on the prompt hot path. A non-Ready verdict already covers the
	// unreadable case — that check is Blocking — so there is no separate
	// nil test to make here.
	ready := evolve.ExclusionReadiness(skillsDir, deployedDir, coveragePath)
	if !ready.Ready() {
		return nil
	}
	return ready.Coverage.CoveredIDs()
}
