package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/observe"
	"github.com/ikeikeikeike/bough/internal/session"
)

// sessionEndDefaultWindow bounds how many recent observations the
// SessionEnd evaluation reads. Shared by the `bough session-end`
// command and the `bough hook handle --event SessionEnd` dispatch.
const sessionEndDefaultWindow = 500

// runSessionEnd is the shared SessionEnd body: read the project's
// observations, reinforce / demote instinct confidence, append the
// score, and print the summary. Project resolution goes through
// resolveMonorepoRoot so it pools into the same monorepo project the
// hook writes observations to (v0.9.10 ECC model) — before that, a
// sub-repo session-end resolved a different id than the writer and
// saw zero observations. Pure filesystem; no claude --print call.
func runSessionEnd(out io.Writer, root, sessionID string, window int) error {
	cwd := root
	if cwd == "" {
		w, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("session-end: getwd: %w", err)
		}
		cwd = w
	}
	ident, err := homunculus.DetectIdentity(resolveMonorepoRoot(cwd))
	if err != nil {
		return nil // non-git / unresolvable → clean no-op
	}
	if window <= 0 {
		window = sessionEndDefaultWindow
	}
	layout := homunculus.NewLayout()
	obs, _ := observe.TailN(layout.ObservationsFile(ident.ID), window)
	res, err := session.Evaluate(layout, ident.ID, sessionID, obs, time.Now())
	if err != nil {
		return err
	}
	if err := session.AppendScore(layout, ident.ID, res); err != nil {
		return err
	}
	fmt.Fprint(out, session.Summary(res, obs))
	return nil
}

// runPreserveInstincts is the shared PreCompact body: snapshot the
// top-confidence instincts to MEMORY.md (durable) AND print the block
// to stdout for transcript visibility. NOTE: a PreCompact hook's stdout
// does NOT reach the post-compaction model context (only SessionStart/
// Setup inject via stdout); the durable MEMORY.md plus the UserPromptSubmit
// inject on the next prompt are what actually re-surface the instincts.
// Project resolution matches the hook's write target via
// resolveMonorepoRoot. Pure filesystem; no LLM.
func runPreserveInstincts(out io.Writer, root string) error {
	cwd := root
	if cwd == "" {
		w, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("preserve-instincts: getwd: %w", err)
		}
		cwd = w
	}
	ident, err := homunculus.DetectIdentity(resolveMonorepoRoot(cwd))
	if err != nil {
		return nil
	}
	layout := homunculus.NewLayout()
	_, block, err := session.PreserveInstincts(layout, ident.ID, time.Now())
	if err != nil {
		return err
	}
	if block != "" {
		fmt.Fprint(out, block)
	}
	return nil
}

// newSessionEndCmd wires `bough session-end` — the SessionEnd hook
// handler (also dispatched inline by `bough hook handle --event
// SessionEnd`). Pure filesystem; no claude --print call (SessionEnd
// fires once per session, but an LLM call on every close would add
// cost the operator did not ask for — extraction stays opt-in via the
// observer daemon).
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
session's observations, reinforces / demotes the confidence of instincts
the session exercised, rewrites the adjusted instinct files, and appends
the evaluation to eval/scores.jsonl. No claude --print call.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSessionEnd(cmd.OutOrStdout(), root, sessionID, window)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "session id to record in eval/scores.jsonl")
	cmd.Flags().IntVar(&window, "window", sessionEndDefaultWindow, "max recent observations to evaluate against")
	return cmd
}

// newPreserveInstinctsCmd wires `bough preserve-instincts` — the
// PreCompact hook handler (also dispatched inline by `bough hook
// handle --event PreCompact`). Writes the durable MEMORY.md snapshot and
// prints the top instincts to the transcript (PreCompact stdout is not
// injected into the post-compaction context; re-surfacing is via the
// UserPromptSubmit inject on the next prompt).
func newPreserveInstinctsCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "preserve-instincts",
		Short: "Snapshot top instincts to MEMORY.md + stdout before context compaction (PreCompact hook)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPreserveInstincts(cmd.OutOrStdout(), root)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	return cmd
}
