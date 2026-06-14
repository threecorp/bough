package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/ikeikeikeike/bough/internal/registry"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the worktrees registered in .worktree-ports.json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			monorepoRoot, cfg, err := loadConfigAndRoot(cmd, "")
			if err != nil {
				return err
			}
			store := registry.NewStore(
				filepath.Join(monorepoRoot, cfg.Registry.Path),
				cfg.Registry.BackupDir,
			)
			reg, err := store.Load()
			if err != nil {
				return err
			}
			return writeRegistryTable(cmd.OutOrStdout(), reg)
		},
	}
	return cmd
}

// writeRegistryTable renders the registry to an aligned NAME / DB / API
// / GATEWAY / ... table. We discover the kind columns dynamically from
// the registry data so a future plugin (postgres, redis, …) shows up
// without having to update this command.
func writeRegistryTable(stdout io.Writer, reg registry.Registry) error {
	if len(reg) == 0 {
		fmt.Fprintln(stdout, "(registry is empty — `bough create <name>` to populate)")
		return nil
	}
	kindSet := map[string]struct{}{}
	for _, entry := range reg {
		for kind := range entry {
			kindSet[kind] = struct{}{}
		}
	}
	kinds := make([]string, 0, len(kindSet))
	for k := range kindSet {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	sort.Strings(names)

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "NAME\t")
	for _, k := range kinds {
		_, _ = fmt.Fprintf(tw, "%s\t", k)
	}
	_, _ = fmt.Fprintln(tw)
	for _, name := range names {
		_, _ = fmt.Fprintf(tw, "%s\t", name)
		for _, k := range kinds {
			if port, ok := reg[name][k]; ok {
				_, _ = fmt.Fprintf(tw, "%d\t", port)
			} else {
				_, _ = fmt.Fprintf(tw, "-\t")
			}
		}
		_, _ = fmt.Fprintln(tw)
	}
	return tw.Flush()
}

// Compile-time guard: ensure os.Stdout matches the io.Writer surface
// the rest of the package threads around. Acts as documentation more
// than enforcement.
var _ io.Writer = os.Stdout
