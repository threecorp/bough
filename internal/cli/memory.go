package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

func newMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Inspect and operate the configured memory backend",
		Long: `bough memory exposes backend-agnostic operations against the
configured MemoryBackend plugin (SQLite reference-fallback in v0.5,
mem0 / Graphiti in v0.6+).`,
	}
	cmd.AddCommand(
		newMemoryStatusCmd(),
		newMemoryQueryCmd(),
		newMemoryForgetCmd(),
		newMemoryExportCmd(),
	)
	return cmd
}

func newMemoryStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Backend health + the SQLite fallback notice",
		RunE: func(c *cobra.Command, _ []string) error {
			_, cfg, err := loadConfigAndRoot(c, "")
			if err != nil {
				return err
			}
			if !cfg.Instinct.Enabled {
				fmt.Fprintln(c.OutOrStdout(), "instinct subsystem: disabled (set instinct.enabled: true in .bough.yaml)")
				return nil
			}
			backend, kill, role, err := discoverMemoryBackend(cfg)
			if err != nil {
				return err
			}
			defer kill()
			h, err := backend.Health(context.Background(), &memapi.HealthReq{})
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Memory backend: %s (role=%s)\n", h.BackendKind, role)
			fmt.Fprintf(c.OutOrStdout(), "Plugin version: %s\n", h.PluginVersion)
			caps, err := backend.Capabilities(context.Background())
			if err == nil {
				fmt.Fprintf(c.OutOrStdout(), "Capabilities: semantic=%v graph=%v vector=%v metadata=%v\n",
					caps.SemanticQuery, caps.GraphQuery, caps.VectorSearch, caps.SupportsMetadata)
			}
			// Round 3 AI #2/#3/#4 + AI #1: educational notice that
			// the SQLite backend is the reference-fallback, not the
			// production memory engine. Stderr so scripts that
			// parse stdout do not break.
			if h.BackendKind == "sqlite" && role == "reference-fallback" {
				fmt.Fprintln(os.Stderr, "\n[NOTICE] Using reference-fallback SQLite backend.")
				fmt.Fprintln(os.Stderr, "         For production / team scale, consider configuring mem0 or graphiti.")
				fmt.Fprintln(os.Stderr, "         See docs/EXTERNAL_MEMORY_BACKENDS.md")
			}
			return nil
		},
	}
	return cmd
}

func newMemoryQueryCmd() *cobra.Command {
	var term string
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Direct backend query (bypasses the budget aggregator)",
		RunE: func(c *cobra.Command, _ []string) error {
			_, cfg, err := loadConfigAndRoot(c, "")
			if err != nil {
				return err
			}
			backend, kill, _, err := discoverMemoryBackend(cfg)
			if err != nil {
				return err
			}
			defer kill()
			resp, err := backend.Query(context.Background(), &memapi.QueryReq{
				Term:       term,
				MaxResults: cfg.Instinct.Retrieve.MaxResults,
				MaxTokens:  cfg.Instinct.Retrieve.MaxTokens,
			})
			if err != nil {
				return err
			}
			for _, r := range resp.Results {
				fmt.Fprintf(c.OutOrStdout(), "[%s] %s\n", r.Instinct.ID, r.Instinct.Rule)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&term, "term", "", "FTS search term")
	return cmd
}

func newMemoryForgetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "forget <id>",
		Short: "Direct backend forget (soft delete)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			_, cfg, err := loadConfigAndRoot(c, "")
			if err != nil {
				return err
			}
			backend, kill, _, err := discoverMemoryBackend(cfg)
			if err != nil {
				return err
			}
			defer kill()
			_, err = backend.Forget(context.Background(), &memapi.ForgetReq{ID: args[0]})
			return err
		},
	}
	return cmd
}

func newMemoryExportCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the backend in yaml or jsonl",
		RunE: func(c *cobra.Command, _ []string) error {
			_, cfg, err := loadConfigAndRoot(c, "")
			if err != nil {
				return err
			}
			backend, kill, _, err := discoverMemoryBackend(cfg)
			if err != nil {
				return err
			}
			defer kill()
			resp, err := backend.Export(context.Background(), &memapi.ExportReq{Format: format})
			if err != nil {
				return err
			}
			_, _ = c.OutOrStdout().Write(resp.Payload)
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "yaml", "yaml | jsonl")
	return cmd
}
