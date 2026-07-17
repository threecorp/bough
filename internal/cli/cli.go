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
// v0.9 sprint resets the surface to "ECC verbatim port":
//   - per-worktree infrastructure (create / remove / verify / list /
//     status / backfill / config / plugins) — kept
//   - hook auto-wire (= bough hook install/uninstall/list/replay/
//     doctor/handle) — kept
//   - observer + inject + evolve + session-end + instinct — new in v0.9,
//     all backed by Claude Code's `claude --print` subprocess so the
//     LLM cost stays inside the operator's existing Claude Code
//     subscription (= no Anthropic API key, no separate billing).
func NewRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "bough",
		Short:         "Per-worktree isolation + continuous-learning toolkit for Claude Code",
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
		newConfigCmd(),
		newPluginsCmd(),
		// Claude Code integration (v0.18+): the one namespace for what bough
		// installs INTO Claude Code. Kept distinct from `plugins` above, which
		// means bough's own engine plugin binaries.
		newClaudeCmd(),
		// Continuous learning (v0.9+): one namespace, mirroring the single
		// `instinct:` block that configures all of it in .bough.yaml.
		newInstinctCmd(),

		// --- Backwards compatibility ---------------------------------------
		// `bough hook ...` / `bough doctor` predate the `claude` namespace and
		// are wired into operators' settings.json + scripts. They keep working;
		// the notice points at the new path.
		deprecatedAlias(newHookCmd(), "bough claude hook"),
		deprecatedAlias(newDoctorCmd(), "bough claude doctor"),
		// The learning verbs that used to sit at root, before `instinct` became
		// the namespace for the domain. Same contract as the hook aliases: they
		// run, they just say where they went.
		deprecatedAlias(newObserverCmd(), "bough instinct observer"),
		deprecatedAlias(newEvolveCmd(), "bough instinct evolve"),
		deprecatedAlias(newEccCmd(), "bough instinct"),
		// The hook dispatcher's internal verbs. hook.go's handle switch calls
		// their Go functions directly, so these CLI entry points are a manual
		// debugging escape hatch, not part of the advertised surface.
		hiddenCmd(newInjectContextCmd()),
		hiddenCmd(newSessionEndCmd()),
		hiddenCmd(newPreserveInstinctsCmd()),
		hiddenCmd(newSessionEvolveClaudeMDCmd()),
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
touches the host binary.

v0.9 adds the continuous-learning surface ported verbatim from the
upstream affaan-m/everything-claude-code reference implementation:
` + "`bough observe`" + ` (PreToolUse / PostToolUse / Stop hook),
` + "`bough inject-context`" + ` (UserPromptSubmit hook),
` + "`bough observer start`" + ` (background daemon that calls
` + "`claude --model haiku --print`" + ` to extract instincts from
session observations), ` + "`bough evolve --generate`" + ` (5-gate
cluster → SKILL.md), ` + "`bough instinct status`" + `, and the
SessionEnd / PreCompact handlers (` + "`bough session-end`" + `,
` + "`bough preserve-instincts`" + `). All LLM work runs through the
operator's existing Claude Code subscription — no API key, no
separate billing.`
