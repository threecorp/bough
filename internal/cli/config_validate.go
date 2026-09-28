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
		Short: "Validate a .bough.yaml file (default: <cwd>/.bough.yaml; " + v03FallbackCaption + ")",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var path string
			if len(args) == 1 {
				path = args[0]
			} else {
				// loadConfigAndRoot's error is the real cause (missing
				// file, malformed YAML, failed schema validation) — it
				// must propagate, not be discarded in favor of the
				// generic "path missing" message below, which would be
				// actively wrong (the path did resolve) and hide what
				// actually failed.
				monorepoRoot, _, err := loadConfigAndRoot(cmd, "")
				if err != nil {
					return err
				}
				// Already loaded and validated above; loading again would
				// print every warning twice.
				fmt.Fprintf(cmd.OutOrStdout(), "%s: valid\n", resolveConfigPath(cmd, monorepoRoot))
				return nil
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
