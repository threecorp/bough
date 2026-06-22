package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAllEvents_StableOrder pins the canonical event list so a
// future patch adding a new event has to update the test in
// lockstep — keeping the install / uninstall / doctor diff order
// reproducible across runs.
func TestAllEvents_StableOrder(t *testing.T) {
	got := AllEvents()
	want := []HookEvent{
		EventPreToolUse,
		EventPostToolUse,
		EventUserPromptSubmit,
		EventStop,
		EventSessionEnd,
		EventPreCompact,
		EventWorktreeCreate,
		EventWorktreeRemove,
	}
	if len(got) != len(want) {
		t.Fatalf("AllEvents length: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AllEvents[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

// TestManager_Replay_StillSkeleton pins the v0.7.0 sub-phase
// contract. Replay remains a stub until O-1.3 lands the
// fixture-driven dispatcher; install / uninstall / list went live
// in O-1.2 and are exercised by the per-method tests below.
func TestManager_Replay_StillSkeleton(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "settings.json"))
	if _, err := m.Replay(context.Background(), EventPreToolUse, []byte("{}")); !errors.Is(err, ErrNotYetWired) {
		t.Errorf("Replay: want ErrNotYetWired, got %v", err)
	}
}

// TestManager_Install_FreshFile creates the .claude/settings.json
// file from scratch + populates every canonical event. The
// trailing newline + indent format pins the on-disk shape so a
// future patch tweaking the marshaller catches the regression at
// test-time rather than dogfooding-time.
func TestManager_Install_FreshFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	m := New(path)
	if err := m.Install(context.Background(), "bough hook handle"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written settings: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("written settings missing trailing newline")
	}
	set, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, event := range AllEvents() {
		groups := set[event]
		if len(groups) != 1 {
			t.Errorf("%s: expected exactly one bough group, got %d", event, len(groups))
			continue
		}
		if len(groups[0].Hooks) != 1 {
			t.Errorf("%s: expected one HookEntry, got %d", event, len(groups[0].Hooks))
			continue
		}
		if got, want := groups[0].Hooks[0].Command, CanonicalCommand(event); got != want {
			t.Errorf("%s: command got %q want %q", event, got, want)
		}
	}
}

// TestManager_Install_Idempotent re-runs Install on an already-
// wired file and verifies the file contents are byte-identical
// the second time. Idempotency is the single most important
// property of the auto-wire: hand-running `bough hook install`
// twice (or running it after another tool's reconciliation pass)
// must not duplicate bough's entries.
func TestManager_Install_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	m := New(path)
	if err := m.Install(context.Background(), "bough hook handle"); err != nil {
		t.Fatalf("Install#1: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read #1: %v", err)
	}
	if err := m.Install(context.Background(), "bough hook handle"); err != nil {
		t.Fatalf("Install#2: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read #2: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("Install was not idempotent: first=%q second=%q", string(first), string(second))
	}
}

// TestManager_Install_PreservesHandEdited writes a hand-edited
// entry first, then runs Install + Uninstall. The hand-edited
// entry must survive both passes — bough only touches groups it
// wholly owns.
func TestManager_Install_PreservesHandEdited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	handEdited := `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Edit", "hooks": [{"type": "command", "command": "echo hand-edited"}]}
    ]
  }
}
`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(handEdited), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	m := New(path)
	if err := m.Install(context.Background(), "bough hook handle"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	set, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List after Install: %v", err)
	}
	groups := set[EventPreToolUse]
	if len(groups) != 2 {
		t.Fatalf("PreToolUse: expected 2 groups (hand-edited + bough), got %d", len(groups))
	}
	foundHand := false
	for _, g := range groups {
		if !isBoughGroup(g) {
			if len(g.Hooks) == 1 && g.Hooks[0].Command == "echo hand-edited" {
				foundHand = true
			}
		}
	}
	if !foundHand {
		t.Errorf("hand-edited group was clobbered: %+v", groups)
	}

	// Uninstall must remove only the bough group, leaving the hand-edited one.
	if err := m.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	set, err = m.List(context.Background())
	if err != nil {
		t.Fatalf("List after Uninstall: %v", err)
	}
	groups = set[EventPreToolUse]
	if len(groups) != 1 {
		t.Fatalf("PreToolUse after Uninstall: expected 1 group (hand-edited only), got %d", len(groups))
	}
	if len(groups[0].Hooks) != 1 || groups[0].Hooks[0].Command != "echo hand-edited" {
		t.Errorf("hand-edited entry not preserved after Uninstall: %+v", groups[0])
	}
}

// TestManager_Uninstall_PreservesOtherFields runs Install + Uninstall
// against a settings.json that also carries unrelated keys (e.g.
// `theme`, `mcpServers`). Those keys must round-trip untouched —
// bough's reconciliation only owns the `hooks` key.
func TestManager_Uninstall_PreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := `{
  "theme": "dark",
  "mcpServers": {"foo": {"command": "foo-mcp"}}
}
`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	m := New(path)
	if err := m.Install(context.Background(), "bough hook handle"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := m.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if raw["theme"] != "dark" {
		t.Errorf("theme clobbered: %v", raw["theme"])
	}
	if _, ok := raw["mcpServers"]; !ok {
		t.Errorf("mcpServers clobbered: %v", raw)
	}
	if _, ok := raw["hooks"]; ok {
		t.Errorf("hooks key should be removed after Uninstall when no hand-edited groups remain: %v", raw)
	}
}

// TestManager_List_MissingFile asserts a fresh repo returns an
// empty HookSet without erroring.
func TestManager_List_MissingFile(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "missing", "settings.json"))
	set, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List on missing file: %v", err)
	}
	if len(set) != 0 {
		t.Errorf("expected empty HookSet on missing file, got %+v", set)
	}
}

// TestManager_List_ParsesExistingHandEdited reads a settings.json
// the operator authored by hand and verifies bough's decoder
// round-trips its matcher groups untouched.
func TestManager_List_ParsesExistingHandEdited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Edit|Write", "hooks": [{"type": "command", "command": "echo before-edit"}]},
      {"hooks": [{"type": "command", "command": "echo any-tool"}]}
    ]
  }
}
`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	m := New(path)
	set, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	groups := set[EventPreToolUse]
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if groups[0].Matcher != "Edit|Write" {
		t.Errorf("first group matcher: got %q want %q", groups[0].Matcher, "Edit|Write")
	}
	if groups[1].Matcher != "" {
		t.Errorf("second group matcher: got %q want empty", groups[1].Matcher)
	}
}
