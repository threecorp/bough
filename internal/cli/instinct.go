package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/observer"
	"github.com/ikeikeikeike/bough/pkg/schema"
)

func newInstinctCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "instinct",
		Short: "Mint, approve, query, and forget per-worktree instincts",
		Long: `bough instinct manages the v0.5 instinct subsystem: behavioural
rules and observations the host accumulates per worktree, repo,
and global scope.

The subsystem is opt-in. Set instinct.enabled: true in .bough.yaml
to use it. See docs/INSTINCTS.md for the lifecycle.`,
	}
	cmd.AddCommand(
		newInstinctStatusCmd(),
		newInstinctMintCmd(),
		newInstinctIngestCmd(),
		newInstinctApproveCmd(),
		newInstinctQueryCmd(),
		newInstinctForgetCmd(),
		newInstinctPromoteCmd(),
		newInstinctExportCmd(),
		newInstinctImportCmd(),
	)
	return cmd
}

func newInstinctStatusCmd() *cobra.Command {
	var scopeArg string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the active instinct counts per scope",
		RunE: func(c *cobra.Command, _ []string) error {
			coord, close, err := loadInstinctCoordinator(c)
			if err != nil {
				return err
			}
			defer close()
			_, cfg, _ := loadConfigAndRoot(c, "")
			scope := currentScope(cfg, "", scopeArg)
			results, err := coord.Query(noopCtx(), "", scope)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Scope: %s/%s/%s\n", scope.Level, scope.WorktreeID, scope.RepoName)
			fmt.Fprintf(c.OutOrStdout(), "Items: %d returned (max_results=%d, max_tokens=%d)\n",
				len(results), cfg.Instinct.Retrieve.MaxResults, cfg.Instinct.Retrieve.MaxTokens)
			return nil
		},
	}
	cmd.Flags().StringVar(&scopeArg, "worktree", "", "worktree id (default: derived from cwd)")
	return cmd
}

func newInstinctMintCmd() *cobra.Command {
	var (
		rule   string
		source string
	)
	cmd := &cobra.Command{
		Use:   "mint",
		Short: "Mint a single instinct from an explicit rule string",
		RunE: func(c *cobra.Command, _ []string) error {
			if rule == "" {
				return fmt.Errorf("--rule is required")
			}
			coord, close, err := loadInstinctCoordinator(c)
			if err != nil {
				return err
			}
			defer close()
			_, cfg, _ := loadConfigAndRoot(c, "")
			scope := currentScope(cfg, "", "")
			bundle := schema.TraceBundle{
				Source:  schema.TraceSource(source),
				Scope:   scope,
				Content: rule,
			}
			admitted, _, err := coord.Ingest(noopCtx(), scope, []schema.TraceBundle{bundle})
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "minted %d candidate(s); approve with `bough instinct approve <id>`\n", admitted)
			return nil
		},
	}
	cmd.Flags().StringVar(&rule, "rule", "", "the behavioural rule string")
	cmd.Flags().StringVar(&source, "source", "explicit_user_feedback", "the trace source classification")
	return cmd
}

func newInstinctIngestCmd() *cobra.Command {
	var (
		source        string
		sourceEventID string
	)
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Ingest a stream of trace lines from stdin",
		Long: `Read stdin and group lines into TraceBundles for the coordinator.

Examples:

  make test 2>&1 | bough instinct ingest --stdin --source test_failure
  go test ./... 2>&1 | bough instinct ingest --stdin --source test_failure

--source-event-id makes the ingest idempotent: a CI rerun with the
same id and identical payload produces no new rows.`,
		RunE: func(c *cobra.Command, _ []string) error {
			coord, close, err := loadInstinctCoordinator(c)
			if err != nil {
				return err
			}
			defer close()
			_, cfg, _ := loadConfigAndRoot(c, "")
			scope := currentScope(cfg, "", "")
			admitted, reinforced, err := observer.Ingest(noopCtx(), coord, os.Stdin, observer.StdinIngestOptions{
				Source:        schema.TraceSource(source),
				Scope:         scope,
				SourceEventID: sourceEventID,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "ingest: %d admitted, %d reinforced (source=%s)\n",
				admitted, reinforced, source)
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "stdin", "trace source classification")
	cmd.Flags().StringVar(&sourceEventID, "source-event-id", "", "idempotency token (recommended for CI)")
	_ = cmd.Flags().Bool("stdin", true, "read from stdin (placeholder for ergonomics; stdin is always read)")
	return cmd
}

func newInstinctApproveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approve <id>",
		Short: "Promote a candidate row to active",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			coord, close, err := loadInstinctCoordinator(c)
			if err != nil {
				return err
			}
			defer close()
			_, cfg, _ := loadConfigAndRoot(c, "")
			scope := currentScope(cfg, "", "")
			results, err := coord.Query(noopCtx(), "", scope)
			if err != nil {
				return err
			}
			for _, r := range results {
				if r.ID == args[0] {
					return coord.ApproveInstinct(noopCtx(), r)
				}
			}
			return fmt.Errorf("instinct %q not found in scope %s/%s", args[0], scope.Level, scope.WorktreeID)
		},
	}
	return cmd
}

func newInstinctQueryCmd() *cobra.Command {
	var term string
	cmd := &cobra.Command{
		Use:   "query",
		Short: "FTS search across stored instincts",
		RunE: func(c *cobra.Command, _ []string) error {
			coord, close, err := loadInstinctCoordinator(c)
			if err != nil {
				return err
			}
			defer close()
			_, cfg, _ := loadConfigAndRoot(c, "")
			scope := currentScope(cfg, "", "")
			results, err := coord.Query(noopCtx(), term, scope)
			if err != nil {
				return err
			}
			for _, r := range results {
				fmt.Fprintf(c.OutOrStdout(), "[%s] %s (conf=%.2f, hits=%d)\n", r.ID, r.Rule, r.Confidence, r.Hits)
			}
			fmt.Fprintf(c.OutOrStdout(), "(%d results)\n", len(results))
			return nil
		},
	}
	cmd.Flags().StringVar(&term, "term", "", "FTS search term")
	return cmd
}

func newInstinctForgetCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "forget <id>",
		Short: "Soft-delete an instinct (state → forgotten)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			coord, close, err := loadInstinctCoordinator(c)
			if err != nil {
				return err
			}
			defer close()
			_, cfg, _ := loadConfigAndRoot(c, "")
			scope := currentScope(cfg, "", "")
			if reason == "" {
				reason = "explicit forget"
			}
			return coord.Forget(noopCtx(), args[0], scope, reason)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "audit-log reason for the forget")
	return cmd
}

func newInstinctPromoteCmd() *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:   "promote <id> --to repo|global",
		Short: "Promote an instinct to a higher scope tier",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			coord, close, err := loadInstinctCoordinator(c)
			if err != nil {
				return err
			}
			defer close()
			_, cfg, _ := loadConfigAndRoot(c, "")
			scope := currentScope(cfg, "", "")
			results, err := coord.Query(noopCtx(), "", scope)
			if err != nil {
				return err
			}
			for _, r := range results {
				if r.ID == args[0] {
					target := schema.ScopeLevel(to)
					return coord.Promote(noopCtx(), r, target)
				}
			}
			return fmt.Errorf("instinct %q not found in scope %s/%s", args[0], scope.Level, scope.WorktreeID)
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "target scope: repo | global")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func newInstinctExportCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export instincts in yaml or jsonl",
		RunE: func(c *cobra.Command, _ []string) error {
			return fmt.Errorf("export: not yet wired (Μ-1.12); use `bough memory export` for now")
		},
	}
	cmd.Flags().StringVar(&format, "format", "yaml", "yaml | jsonl")
	return cmd
}

func newInstinctImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Import previously-exported instincts",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return fmt.Errorf("import: not yet wired (Μ-1.12); use `bough memory import` for now")
		},
	}
	return cmd
}
