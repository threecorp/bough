package cli

import (
	"slices"
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

// TestInstinctNamespaceIsTheCanonicalHome pins the second grouping: the
// continuous-learning verbs that used to sit at root now live under `instinct`,
// mirroring the single `instinct:` block that configures them in .bough.yaml.
// The root spellings still work and name where they went.
func TestInstinctNamespaceIsTheCanonicalHome(t *testing.T) {
	root := NewRootCmd("0.0.0-test")

	instinct := findCmd(t, root, "instinct")
	if instinct.Deprecated != "" {
		t.Error("`bough instinct` must not be deprecated; it is the replacement")
	}
	for _, path := range [][]string{
		{"status"}, {"list"}, {"show"}, {"promote"}, // unchanged spellings
		{"observer", "run-once"}, {"observer", "start"}, {"observer", "stop"}, {"observer", "status"},
		{"evolve"}, {"import"},
	} {
		if d := findCmd(t, instinct, path...).Deprecated; d != "" {
			t.Errorf("`bough instinct %s` is deprecated (%q); it is the canonical spelling",
				strings.Join(path, " "), d)
		}
	}

	// The root aliases point at those homes, naming the exact line to retype.
	for _, tc := range []struct {
		path []string
		want string
	}{
		{[]string{"observer"}, "bough instinct observer"},
		{[]string{"observer", "run-once"}, "bough instinct observer run-once"},
		{[]string{"observer", "start"}, "bough instinct observer start"},
		{[]string{"evolve"}, "bough instinct evolve"},
		{[]string{"ecc"}, "bough instinct"},
		{[]string{"ecc", "import"}, "bough instinct import"},
	} {
		d := findCmd(t, root, tc.path...).Deprecated
		if d == "" {
			t.Errorf("`bough %s` carries no deprecation notice", strings.Join(tc.path, " "))
			continue
		}
		if !strings.Contains(d, tc.want) {
			t.Errorf("`bough %s` notice does not name %q: %s", strings.Join(tc.path, " "), tc.want, d)
		}
	}
}

// TestRootSurfaceStaysSmall is the point of both groupings, stated as a number.
// The root is what an operator reads first; every entry there costs attention,
// so a new one should be a deliberate choice rather than the default landing
// spot for whatever gets built next.
func TestRootSurfaceStaysSmall(t *testing.T) {
	root := NewRootCmd("0.0.0-test")

	var advertised []string
	for _, c := range root.Commands() {
		if c.IsAvailableCommand() { // excludes hidden AND deprecated
			advertised = append(advertised, c.Name())
		}
	}
	// 12 today: backfill claude completion config create help instinct list
	//           plugins remove status verify
	if len(advertised) > 12 {
		t.Errorf("root advertises %d commands (%v); it was cut to 12 on purpose — group the new one or raise this bound deliberately",
			len(advertised), advertised)
	}
	// The two namespaces must be among them, or the grouping did not happen.
	for _, want := range []string{"claude", "instinct"} {
		if !slices.Contains(advertised, want) {
			t.Errorf("root does not advertise %q: %v", want, advertised)
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
