package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The bough-hooks / bough-all Claude Code plugins ship this repo's
// claude-plugins/bough-hooks/hooks/hooks.json, which wires every hook
// event to the same dispatcher `bough claude hook install` writes into
// settings.json. That file is hand-authored, so it can silently drift
// from CanonicalCommand / AllEvents when the event set changes. These
// tests are the guard: the committed plugin manifest must mirror the
// canonical wiring exactly, in both directions.

// repoRoot resolves the module root from this test file's own location,
// so the tests are independent of the working directory `go test` runs in.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate repo root")
	}
	// thisFile = <repoRoot>/internal/hooks/plugin_sync_test.go
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

// pluginHooksPath points at the one committed copy of the manifest.
// bough-all does not carry a second copy — it symlinks this directory
// (see TestBoughAllSharesOneHooksManifest), so guarding this file guards
// both plugins.
func pluginHooksPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "claude-plugins", "bough-hooks", "hooks", "hooks.json")
}

// loadPluginHooks reads + parses the committed plugin hooks.json via the
// same decodeHookSet the production settings.json path uses (Manager's
// loadSettings/List), so this drift guard exercises the actual decode
// logic instead of a bespoke parser that could silently diverge from it.
func loadPluginHooks(t *testing.T) HookSet {
	t.Helper()
	data, err := os.ReadFile(pluginHooksPath(t))
	if err != nil {
		t.Fatalf("read plugin hooks.json: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse plugin hooks.json: %v", err)
	}
	set, err := decodeHookSet(raw)
	if err != nil {
		t.Fatalf("decode plugin hooks.json: %v", err)
	}
	return set
}

// diffPluginHooks reports every way `got` (the parsed plugin
// hooks.json event map) diverges from the canonical wiring: a missing
// event, an event whose wired command is not CanonicalCommand(event),
// or an event present in the manifest but absent from AllEvents().
// An empty result means the manifest is an exact mirror.
func diffPluginHooks(got map[HookEvent][]HookGroup) []string {
	var problems []string
	want := make(map[HookEvent]bool, len(AllEvents()))
	for _, ev := range AllEvents() {
		want[ev] = true
		groups := got[ev]
		if len(groups) == 0 {
			problems = append(problems, fmt.Sprintf("event %s: missing from plugin hooks.json", ev))
			continue
		}
		found := false
		for _, g := range groups {
			for _, e := range g.Hooks {
				if strings.TrimSpace(e.Command) == CanonicalCommand(ev) {
					found = true
				}
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("event %s: no entry with command %q", ev, CanonicalCommand(ev)))
		}
	}
	for ev := range got {
		if !want[ev] {
			problems = append(problems, fmt.Sprintf("event %s: present in plugin hooks.json but not in AllEvents()", ev))
		}
	}
	return problems
}

// TestPluginHooksMatchCanonical is the normal-path guard: the shipped
// hooks/hooks.json must mirror CanonicalCommand for every AllEvents()
// event and declare no extras.
func TestPluginHooksMatchCanonical(t *testing.T) {
	got := loadPluginHooks(t)
	if problems := diffPluginHooks(got); len(problems) > 0 {
		t.Fatalf("plugin hooks.json drifted from the canonical wiring:\n  %s", strings.Join(problems, "\n  "))
	}
}

// TestBoughAllSharesOneHooksManifest pins the packaging decision that makes
// the guard above sufficient: bough-all must reach the manifest through a
// symlink to bough-hooks, never a second copy. A copy would pass the drift
// test above (it only reads bough-hooks) while shipping stale wiring to every
// bough-all user — the exact failure the guard exists to prevent.
func TestBoughAllSharesOneHooksManifest(t *testing.T) {
	allHooks := filepath.Join(repoRoot(t), "claude-plugins", "bough-all", "hooks")

	fi, err := os.Lstat(allHooks)
	if err != nil {
		t.Fatalf("lstat bough-all/hooks: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("bough-all/hooks must be a symlink to bough-hooks/hooks, got mode %s "+
			"(a real directory means a second manifest copy that this package's drift test cannot see)", fi.Mode())
	}

	got, err := filepath.EvalSymlinks(filepath.Join(allHooks, "hooks.json"))
	if err != nil {
		t.Fatalf("resolve bough-all/hooks/hooks.json: %v", err)
	}
	want, err := filepath.EvalSymlinks(pluginHooksPath(t))
	if err != nil {
		t.Fatalf("resolve bough-hooks/hooks/hooks.json: %v", err)
	}
	if got != want {
		t.Fatalf("bough-all resolves to a different manifest\n got: %s\nwant: %s", got, want)
	}
}

// TestDiffPluginHooksDetectsDrift is the failure-path guard: a
// manifest that is missing an event, wires a wrong command, or adds an
// unknown event must be reported. If diffPluginHooks ever went silent,
// the normal-path test above would rot into a rubber stamp.
func TestDiffPluginHooksDetectsDrift(t *testing.T) {
	canonical := func() map[HookEvent][]HookGroup {
		m := map[HookEvent][]HookGroup{}
		for _, ev := range AllEvents() {
			m[ev] = []HookGroup{{Hooks: []HookEntry{{Type: "command", Command: CanonicalCommand(ev)}}}}
		}
		return m
	}

	t.Run("missing event", func(t *testing.T) {
		m := canonical()
		delete(m, EventPreCompact)
		if len(diffPluginHooks(m)) == 0 {
			t.Fatal("expected a missing event to be reported")
		}
	})

	t.Run("wrong command", func(t *testing.T) {
		m := canonical()
		m[EventUserPromptSubmit] = []HookGroup{{Hooks: []HookEntry{{Type: "command", Command: "bough hook handle --event Bogus"}}}}
		if len(diffPluginHooks(m)) == 0 {
			t.Fatal("expected a wrong command to be reported")
		}
	})

	t.Run("unknown extra event", func(t *testing.T) {
		m := canonical()
		m["NotARealEvent"] = []HookGroup{{Hooks: []HookEntry{{Type: "command", Command: "bough hook handle --event NotARealEvent"}}}}
		if len(diffPluginHooks(m)) == 0 {
			t.Fatal("expected an unknown extra event to be reported")
		}
	})

	t.Run("canonical passes", func(t *testing.T) {
		if problems := diffPluginHooks(canonical()); len(problems) > 0 {
			t.Fatalf("a canonical map must not be flagged: %v", problems)
		}
	})
}
