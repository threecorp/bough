package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/inject"
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
	)
	cmd := &cobra.Command{
		Use:   "inject-context",
		Short: "Print the confidence-ranked instinct block for the UserPromptSubmit hook",
		Long: `bough inject-context is the UserPromptSubmit hook handler. It
prints the highest-confidence instincts for the current project (+
global scope) so Claude Code folds them into the next turn's context.

The block is byte-capped (default ~9.5 KB) because the stdout is
billed as input tokens; instincts are confidence-sorted so the most
reliable ones land before the cap truncates. No claude --print call
is made — selection is pure filesystem.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := inject.Options{
				MaxBytes:     maxBytes,
				MaxInstincts: maxN,
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
	return cmd
}
