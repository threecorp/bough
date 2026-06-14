// Package cli wires together the bough host's cobra subcommands. The
// command tree intentionally favours composition over inheritance — each
// subcommand is its own *cobra.Command in its own file so a future
// `bough <new-verb>` adds one file and one AddCommand line, no central
// switch to update.
package cli

import (
	"github.com/spf13/cobra"
)

// NewRootCmd assembles the full bough command tree. `version` is
// surfaced through `bough --version`; main.go fills it in from the
// linker-injected build tag.
func NewRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "bough",
		Short:         "Per-worktree isolation orchestrator",
		Long:          longRootDescription,
		Version:       version,
		SilenceUsage:  true, // RunE-returned errors print without the usage banner
		SilenceErrors: true, // main.go formats the error itself
	}
	// Persistent flag visible to every subcommand. Empty default means
	// "look for `<monorepoRoot>/.worktree-isolation.yaml`". The
	// monorepo root is resolved per subcommand from --stdin-json's cwd
	// or from os.Getwd() when invoked interactively.
	root.PersistentFlags().String("config", "", "path to .worktree-isolation.yaml (default: <monorepoRoot>/.worktree-isolation.yaml)")

	root.AddCommand(
		newCreateCmd(),
		newRemoveCmd(),
		newVerifyCmd(),
		newListCmd(),
		newStatusCmd(),
		newBackfillCmd(),
		newConfigCmd(),
		newPluginsCmd(),
	)
	return root
}

const longRootDescription = `bough bootstraps per-worktree isolated dev environments declared in
.worktree-isolation.yaml at the monorepo root. Designed to be the
WorktreeCreate / WorktreeRemove hook target for Claude Code's
` + "`claude --worktree`" + ` workflow, bough deterministically allocates a port
triplet (db / api / gateway / ...) per branch, writes the matching
.env.local in every sub-repo, and spawns the configured database engine
via a Hashicorp go-plugin gRPC plugin so adding Postgres / Redis /
Elasticsearch never touches the host binary.`
