package cli

import (
	"github.com/spf13/cobra"
)

// newClaudeCmd is the single namespace for everything bough installs INTO
// Claude Code — hooks today, skills / commands as they land. It exists for two
// reasons the flat root could not solve:
//
//  1. Naming. `bough plugins` already means "the bough-plugin-<kind> ENGINE
//     binaries on PATH" (see plugins_list.go). Claude Code's own extension unit
//     is also called a "plugin", so a `bough plugin ...` verb would sit one
//     letter away from an unrelated command. Grouping the Claude Code surface
//     under `claude` keeps the two vocabularies apart by construction.
//
//  2. Altitude. The hook dispatcher's internal verbs (inject-context /
//     session-end / preserve-instincts / session-evolve-claudemd) were exposed
//     at root next to create/remove/list, so `bough --help` mixed "things an
//     operator runs" with "things a hook fires". They stay reachable (hidden
//     aliases in cli.go) but are no longer part of the advertised surface.
//
// The subcommands are constructed fresh here rather than shared with the root
// aliases: cobra mutates Parent() on AddCommand, so one *cobra.Command instance
// cannot hang off two parents.
func newClaudeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claude",
		Short: "Manage what bough installs into Claude Code (hooks / skills / commands)",
		Long: `bough claude groups every artifact bough installs into Claude Code.

The three kinds differ in when they act, which is why they install
separately rather than as one blob:

  hook     fires automatically on session events (observe / inject /
           evolve / preserve, plus WorktreeCreate/Remove). Scoped per
           project because it runs on EVERY event in that repo.
  skill    model-invoked; inert until Claude decides it is relevant.
  command  operator-invoked (/bough:<name>); inert until typed.

Because skills and commands only act when invoked, they are safe to
install broadly; hooks are not, so they default to project scope.`,
	}
	cmd.AddCommand(
		newHookCmd(),
		newSkillCmd(),
		newCommandCmd(),
		newDoctorCmd(),
	)
	return cmd
}

// deprecatedAlias marks a root-level copy of a command that has moved under
// `bough claude`. The command keeps working verbatim (cobra still executes a
// Deprecated command; it only prints the notice first), so existing muscle
// memory, scripts, and anything an operator already wired into settings.json
// survive the move. Callers pass the replacement path so the notice tells the
// operator exactly where the verb went.
//
// The mark has to recurse. cobra prints the notice for the command it actually
// executes, so marking only `hook` would announce the move to someone typing
// bare `bough hook` — while `bough hook install`, the line that is actually in
// people's muscle memory and scripts, would move silently and never tell them.
// Each subcommand gets its own replacement path so the notice names the exact
// command to type, not just the namespace it moved to.
//
// Hidden subcommands are skipped, and `bough hook handle` is why. It is not an
// operator verb that moved — it is the live dispatcher hooks.CanonicalCommand
// still wires into settings.json, invoked by Claude Code on every session
// event. Deprecating it would print a notice to stderr on every event, telling
// nobody (no human typed it) at the cost of noise in every session. Hidden is
// exactly the signal for "machine-invoked", so it is the right gate.
func deprecatedAlias(cmd *cobra.Command, replacement string) *cobra.Command {
	cmd.Deprecated = "use `" + replacement + "` instead (this alias still works and will be kept for the v0.x line)"
	for _, sub := range cmd.Commands() {
		if sub.Hidden {
			continue
		}
		deprecatedAlias(sub, replacement+" "+sub.Name())
	}
	return cmd
}

// hiddenCmd drops a command from `--help` without unwiring it. Used for the
// hook dispatcher's internal verbs: the dispatcher calls their Go functions
// directly (see hook.go's handle switch), so the CLI entry points exist only as
// a manual escape hatch for debugging — valuable to keep, noise to advertise.
func hiddenCmd(cmd *cobra.Command) *cobra.Command {
	cmd.Hidden = true
	return cmd
}
