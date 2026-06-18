package cli

import (
	"fmt"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Operations against the .bough.yaml schema",
	}
	cmd.AddCommand(newConfigValidateCmd())
	return cmd
}

func newConfigValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate a .bough.yaml file (default: <cwd>/.bough.yaml; v0.3 .worktree-isolation.yaml accepted on fallback)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var path string
			if len(args) == 1 {
				path = args[0]
			} else {
				monorepoRoot, _, err := loadConfigAndRoot(cmd, "")
				if err == nil {
					path = resolveConfigPath(cmd, monorepoRoot)
				}
			}
			if path == "" {
				return fmt.Errorf("path argument missing and could not be resolved from cwd")
			}
			if _, err := config.Load(path); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: valid\n", path)
			return nil
		},
	}
	return cmd
}
