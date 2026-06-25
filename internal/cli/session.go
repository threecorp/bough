package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/observe"
	"github.com/ikeikeikeike/bough/internal/session"
)

// newSessionEndCmd wires `bough session-end` — the SessionEnd hook
// handler. It summarises the session's observations, reinforces /
// demotes instinct confidence based on token overlap with the
// session, and appends the result to eval/scores.jsonl. Pure
// filesystem; no claude --print call (= SessionEnd fires once per
// session, but reinforcing it with an LLM call on every close would
// add cost the operator did not ask for).
func newSessionEndCmd() *cobra.Command {
	var (
		root      string
		sessionID string
		window    int
	)
	cmd := &cobra.Command{
		Use:   "session-end",
		Short: "Summarise the session + evaluate instinct confidence (SessionEnd hook)",
		Long: `bough session-end is the SessionEnd hook handler. It reads the
session's observations, reinforces the confidence of instincts the
session exercised (and leaves the rest unchanged), rewrites the
adjusted instinct files, and appends the evaluation to
eval/scores.jsonl. No claude --print call.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd := root
			if cwd == "" {
				w, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("session-end: getwd: %w", err)
				}
				cwd = w
			}
			ident, err := homunculus.DetectIdentity(cwd)
			if err != nil {
				return nil // non-git → clean no-op
			}
			layout := homunculus.NewLayout()
			obs, _ := observe.TailN(layout.ObservationsFile(ident.ID), window)
			now := time.Now()
			res, err := session.Evaluate(layout, ident.ID, sessionID, obs, now)
			if err != nil {
				return err
			}
			if err := session.AppendScore(layout, ident.ID, res); err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), session.Summary(res, obs))
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "session id to record in eval/scores.jsonl")
	cmd.Flags().IntVar(&window, "window", 500, "max recent observations to evaluate against")
	return cmd
}

// newPreserveInstinctsCmd wires `bough preserve-instincts` — the
// PreCompact hook handler. It snapshots the top-confidence instincts
// into MEMORY.md so a context compaction does not lose the operator's
// most-reliable learned patterns. Pure filesystem; no LLM.
func newPreserveInstinctsCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "preserve-instincts",
		Short: "Snapshot top instincts to MEMORY.md before context compaction (PreCompact hook)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd := root
			if cwd == "" {
				w, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("preserve-instincts: getwd: %w", err)
				}
				cwd = w
			}
			ident, err := homunculus.DetectIdentity(cwd)
			if err != nil {
				return nil
			}
			layout := homunculus.NewLayout()
			path, err := session.PreserveInstincts(layout, ident.ID, time.Now())
			if err != nil {
				return err
			}
			if path == "" {
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "preserved top instincts to %s\n", path)
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	return cmd
}
