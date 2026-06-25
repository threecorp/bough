package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/inject"
)

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
			cwd := root
			if cwd == "" {
				w, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("inject-context: getwd: %w", err)
				}
				cwd = w
			}
			ident, err := homunculus.DetectIdentity(cwd)
			if err != nil {
				// A non-git directory is not an error for a hook — just
				// emit nothing so the prompt is unaffected.
				return nil
			}
			layout := homunculus.NewLayout()
			project, _ := homunculus.ScanInstincts(layout.InstinctsDir(ident.ID))
			global, _ := homunculus.ScanInstincts(layout.GlobalInstinctsDir())

			block, n := inject.Build(project, global, inject.Options{
				MaxBytes:      maxBytes,
				MaxInstincts:  maxN,
				MinConfidence: minConf,
			})
			if n == 0 {
				return nil // no qualifying instincts → clean no-op
			}
			fmt.Fprint(cmd.OutOrStdout(), block)
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().IntVar(&maxBytes, "max-bytes", 0, "byte cap on the injected block (default 9500)")
	cmd.Flags().IntVar(&maxN, "max-instincts", 0, "max instincts to consider (default 40)")
	cmd.Flags().Float64Var(&minConf, "min-confidence", 0, "drop instincts below this confidence (default 0.50)")
	return cmd
}
