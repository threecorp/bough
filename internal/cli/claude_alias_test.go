package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/hooks"
)

// findCmd walks a command tree by path, e.g. ("hook", "install").
func findCmd(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	cur := root
	for _, name := range path {
		next := (*cobra.Command)(nil)
		for _, sub := range cur.Commands() {
			if sub.Name() == name {
				next = sub
				break
			}
		}
		if next == nil {
			t.Fatalf("command %q not found under %q", name, cur.CommandPath())
		}
		cur = next
	}
	return cur
}

// TestDeprecatedAliasMarksEveryLeaf is the guard for the notice that carries
// operators through the move to `bough claude`. cobra prints Deprecated for the
// command it EXECUTES, so a mark on `hook` alone reaches only someone typing
// bare `bough hook` — while `bough hook install`, the line actually in scripts
// and muscle memory, would move in silence. Every leaf must announce itself.
func TestDeprecatedAliasMarksEveryLeaf(t *testing.T) {
	root := NewRootCmd("0.0.0-test")

	// The aliased subtree: every descendant of `hook`, not just `hook` itself.
	hook := findCmd(t, root, "hook")
	if hook.Deprecated == "" {
		t.Error("`bough hook` carries no deprecation notice")
	}
	subs := hook.Commands()
	if len(subs) == 0 {
		t.Fatal("`bough hook` has no subcommands; this guard would be vacuous")
	}
	for _, sub := range subs {
		if sub.Hidden {
			continue // machine-invoked; see TestHookHandleIsNotDeprecated
		}
		if sub.Deprecated == "" {
			t.Errorf("`bough hook %s` carries no deprecation notice; operators typing it never learn it moved", sub.Name())
			continue
		}
		// The notice must name the exact command to type, not just the namespace.
		if want := "bough claude hook " + sub.Name(); !strings.Contains(sub.Deprecated, want) {
			t.Errorf("`bough hook %s` notice does not name %q: %s", sub.Name(), want, sub.Deprecated)
		}
	}

	// A childless alias still gets marked.
	if d := findCmd(t, root, "doctor").Deprecated; !strings.Contains(d, "bough claude doctor") {
		t.Errorf("`bough doctor` notice does not name its replacement: %q", d)
	}
}

// TestHookHandleIsNotDeprecated guards the exception. `bough hook handle` is
// what hooks.CanonicalCommand wires into settings.json, so Claude Code invokes
// it on every session event — a deprecation notice there is stderr noise on
// every tool call, delivered to nobody, since no human ever types it. Both
// spellings must stay quiet: the alias Claude Code actually calls, and the
// namespaced one.
func TestHookHandleIsNotDeprecated(t *testing.T) {
	root := NewRootCmd("0.0.0-test")

	for _, path := range [][]string{
		{"hook", "handle"},           // what settings.json wires today
		{"claude", "hook", "handle"}, // the namespaced spelling
	} {
		if d := findCmd(t, root, path...).Deprecated; d != "" {
			t.Errorf("`bough %s` is deprecated (%q); it fires on every session event, so this prints noise to stderr every time",
				strings.Join(path, " "), d)
		}
	}

	// The wiring bough installs must point at a command that is not deprecated:
	// if CanonicalCommand ever moves, this pairing is what catches the mismatch.
	if !strings.Contains(hooks.CanonicalCommand(hooks.EventStop), "hook handle") {
		t.Fatalf("CanonicalCommand no longer calls `hook handle` (%q); this guard needs updating",
			hooks.CanonicalCommand(hooks.EventStop))
	}
}

// TestDeprecatedAliasesStillRun pins the other half of the contract: the notice
// is a heads-up, not a removal. cobra executes a Deprecated command normally, so
// anything an operator already wired into settings.json keeps working — that is
// the whole reason the aliases exist rather than being deleted outright.
func TestDeprecatedAliasesStillRun(t *testing.T) {
	root := NewRootCmd("0.0.0-test")
	for _, path := range [][]string{{"hook"}, {"hook", "install"}, {"doctor"}} {
		cmd := findCmd(t, root, path...)
		if cmd.RunE == nil && cmd.Run == nil && !cmd.HasSubCommands() {
			t.Errorf("`bough %s` is deprecated but has no implementation left to run", strings.Join(path, " "))
		}
	}
}

// TestClaudeNamespaceIsTheCanonicalHome checks the other side of the move: the
// commands the aliases point at actually exist under `bough claude`. A notice
// naming a command that isn't there is worse than no notice.
func TestClaudeNamespaceIsTheCanonicalHome(t *testing.T) {
	root := NewRootCmd("0.0.0-test")
	claude := findCmd(t, root, "claude")
	if claude.Deprecated != "" {
		t.Error("`bough claude` must not be deprecated; it is the replacement")
	}
	for _, path := range [][]string{
		{"hook", "install"}, {"hook", "uninstall"}, {"hook", "list"}, {"hook", "doctor"},
		{"skill", "install"}, {"skill", "uninstall"}, {"skill", "list"},
		{"command", "install"}, {"command", "uninstall"}, {"command", "list"},
		{"doctor"},
	} {
		findCmd(t, claude, path...) // fatals if absent
	}
}
