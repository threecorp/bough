package cli

import (
	"fmt"
	"os"

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
		Short: "Validate a .bough.yaml file (default: <cwd>/.bough.yaml; " + v03FallbackCaption + ")",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var path string
			if len(args) == 1 {
				path = args[0]
			} else {
				// Resolve once, from cwd: monorepo_root moves the root, not the
				// file being validated, and each resolution can print a warning.
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("getwd: %w", err)
				}
				path = resolveConfigPath(cmd, cwd)
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
