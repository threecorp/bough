// Package cli wires together the bough host's cobra subcommands. The
// command tree intentionally favours composition over inheritance — each
// subcommand is its own *cobra.Command in its own file so a future
// `bough <new-verb>` adds one file and one AddCommand line, no central
// switch to update.
package cli

import (
	"github.com/spf13/cobra"
)

// v03FallbackCaption is shared by every --help string that documents
// the v0.3 config-path fallback (root's --config flag, `bough config
// validate`'s Short text, ...) so removing that fallback in v0.5.0
// only needs updating in one place instead of drifting between
// independently-worded copies.
const v03FallbackCaption = "v0.3 .worktree-isolation.yaml accepted on fallback"

// NewRootCmd assembles the full bough command tree. `version` is
// surfaced through `bough --version`; main.go fills it in from the
// linker-injected build tag.
//
// v0.28.0 narrowed the surface back to what the name says: per-worktree
// isolation. The continuous-learning port (observe → instinct → evolve →
// inject, v0.9.0–v0.27.0) is gone; that job belongs to the upstream
// Claude Code plugins built for it. What remains is the worktree
// lifecycle plus the two hook events Claude Code calls to drive it.
func NewRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "bough",
		Short:         "Per-worktree isolated dev environments for monorepos",
		Long:          longRootDescription,
		Version:       version,
		SilenceUsage:  true, // RunE-returned errors print without the usage banner
		SilenceErrors: true, // main.go formats the error itself
	}
	root.PersistentFlags().String("config", "", "path to .bough.yaml (default: <monorepoRoot>/.bough.yaml; "+v03FallbackCaption+")")

	root.AddCommand(
		// Per-worktree infrastructure (v0.4+).
		newCreateCmd(),
		newRemoveCmd(),
		newVerifyCmd(),
		newListCmd(),
		newStatusCmd(),
		newBackfillCmd(),
		newRepairCmd(),
		newConfigCmd(),
		newPluginsCmd(),
		// Claude Code integration (v0.18+): the one namespace for what bough
		// installs INTO Claude Code. Kept distinct from `plugins` above, which
		// means bough's own engine plugin binaries.
		newClaudeCmd(),

		// --- Backwards compatibility ---------------------------------------
		// `bough hook ...` / `bough doctor` predate the `claude` namespace and
		// are wired into operators' settings.json + scripts. They keep working;
		// the notice points at the new path.
		deprecatedAlias(newHookCmd(), "bough claude hook"),
		deprecatedAlias(newDoctorCmd(), "bough claude doctor"),
	)
	return root
}

const longRootDescription = `bough bootstraps per-worktree isolated dev environments declared in
.bough.yaml at the monorepo root. Designed to be the
WorktreeCreate / WorktreeRemove hook target for Claude Code's
` + "`claude --worktree`" + ` workflow, bough deterministically allocates a port
set (db / api / gateway / ...) per branch, writes the matching
.env.local in every sub-repo, and spawns the configured engine
(MySQL / PostgreSQL / Redis / Elasticsearch today; multi-port engines
like rabbitmq / kafka / NATS are first-class in the contract but their
reference plugins are not yet bundled)
via a Hashicorp go-plugin gRPC plugin so adding a new engine never
touches the host binary.`
