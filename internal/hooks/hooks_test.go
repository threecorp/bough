package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// TestManager_Replay_NoHandlersWired returns a non-error
// ReplayResult with a diagnostic Stderr message when settings.json
// has no entries for the requested event — "no handler wired" is a
// legitimate state during install / uninstall cycles and the
// harness should report it, not fail.
func TestManager_Replay_NoHandlersWired(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "settings.json"))
	result, err := m.Replay(context.Background(), EventPreToolUse, []byte("{}"))
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if result == nil {
		t.Fatal("Replay returned nil result")
	}
	if !strings.Contains(result.Stderr, "no hook handlers wired") {
		t.Errorf("expected diagnostic Stderr, got %q", result.Stderr)
	}
}

// TestManager_Replay_ExecutesWiredCommand installs a custom command
// (= `cat` echo so the fixture bytes round-trip through stdout)
// and verifies the Replay harness pipes the fixture into the
// handler's stdin and surfaces the handler's stdout / exit code.
func TestManager_Replay_ExecutesWiredCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// `cat` round-trips stdin to stdout — proves both directions
	// of the pipe wiring.
	seed := `{
  "hooks": {
    "PreToolUse": [
      {"hooks": [{"type": "command", "command": "cat"}]}
    ]
  }
}
`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	m := New(path)
	payload := []byte(`{"hook_event_name":"PreToolUse","fixture":"smoke"}`)
	result, err := m.Replay(context.Background(), EventPreToolUse, payload)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit 0 for cat, got %d (stderr=%q)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, `"fixture":"smoke"`) {
		t.Errorf("stdin was not piped through to stdout: stdout=%q", result.Stdout)
	}
}

// TestManager_Replay_FixturesParse smoke-tests the testdata/
// fixtures so a future patch that breaks the JSON schema gets
// caught at unit-test time.
func TestManager_Replay_FixturesParse(t *testing.T) {
	for _, name := range []string{"PreToolUse.json", "PostToolUse.json", "SessionEnd.json"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Errorf("parse %s: %v", name, err)
			continue
		}
		if parsed["hook_event_name"] == nil {
			t.Errorf("%s missing hook_event_name", name)
		}
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

// TestManager_InstallUninstall_PreservesHandEditedEntryFields runs Install +
// Uninstall against hand-edited entries that carry fields bough does not model
// (timeout, statusMessage, async) and a group key it does not model. They must
// come back unchanged: Claude Code honours them, and dropping a timeout
// silently changes how long a hook may run.
func TestManager_InstallUninstall_PreservesHandEditedEntryFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stop := `[{"matcher":"*","futureGroupKey":true,"hooks":[` +
		`{"type":"command","command":"guard.py","timeout":10,"statusMessage":"checking"},` +
		`{"type":"command","command":"slow.py","timeout":180,"async":true}]}]`
	seed := `{"hooks":{"Stop":` + stop + `}}`
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
	var got struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var want any
	if err := json.Unmarshal([]byte(stop), &want); err != nil {
		t.Fatalf("parse want: %v", err)
	}
	if !reflect.DeepEqual(got.Hooks["Stop"], want) {
		t.Errorf("hand-edited Stop group changed:\n got: %v\nwant: %v", got.Hooks["Stop"], want)
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

// TestManager_Doctor_FreshState reports every event as "not wired"
// against a fresh repo (= no settings.json, no observations.jsonl).
func TestManager_Doctor_FreshState(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), ".claude", "settings.json"))
	report, err := m.Doctor(context.Background(), "")
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if len(report.Events) != len(AllEvents()) {
		t.Fatalf("Events length: got %d want %d", len(report.Events), len(AllEvents()))
	}
	for _, st := range report.Events {
		if st.BoughInstalled || st.HandEdited {
			t.Errorf("%s: expected unwired on fresh state, got bough=%v hand=%v",
				st.Event, st.BoughInstalled, st.HandEdited)
		}
	}
	if report.Cost.DataAvailable {
		t.Errorf("Cost.DataAvailable: expected false on v0.7.0")
	}
}

// TestManager_Doctor_ObserverFromPath is the v0.9.18 regression: doctor's
// observer status comes from the obsPath the caller resolves (the homunculus
// observations.jsonl), NOT a dead working-tree .bough/ probe. A real file with
// N lines → Configured=true + LineCount=N; an empty path → not configured.
func TestManager_Doctor_ObserverFromPath(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), ".claude", "settings.json"))

	obs := filepath.Join(t.TempDir(), "observations.jsonl")
	if err := os.WriteFile(obs, []byte("{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := m.Doctor(context.Background(), obs)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if !report.Observer.Configured {
		t.Errorf("Observer.Configured = false, want true for an existing obs file")
	}
	if report.Observer.LineCount != 3 {
		t.Errorf("Observer.LineCount = %d, want 3", report.Observer.LineCount)
	}

	empty, err := m.Doctor(context.Background(), "")
	if err != nil {
		t.Fatalf("Doctor(empty): %v", err)
	}
	if empty.Observer.Configured {
		t.Errorf("Observer.Configured = true on empty path, want false")
	}
}

// TestManager_Doctor_AfterInstall verifies every event flips to
// BoughInstalled=true after Install, and BoughCommand surfaces the
// canonical command string the render path prints.
func TestManager_Doctor_AfterInstall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	m := New(path)
	if err := m.Install(context.Background(), "bough hook handle"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	report, err := m.Doctor(context.Background(), "")
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	for _, st := range report.Events {
		if !st.BoughInstalled {
			t.Errorf("%s: expected BoughInstalled=true after Install", st.Event)
		}
		if st.HandEdited {
			t.Errorf("%s: expected HandEdited=false on clean install", st.Event)
		}
		if st.BoughCommand == "" {
			t.Errorf("%s: expected BoughCommand non-empty", st.Event)
		}
	}
}

// TestDoctorRender_DoubleFireNote covers the note that catches bough's one
// self-inflicted foot-gun: settings.json and the bough-hooks / bough-all plugin
// wire the same dispatcher, so having both fires every event twice. bough
// cannot read the plugin registry, so the note is the operator's only prompt to
// check. It renders whenever settings.json carries bough hooks (the half bough
// CAN see) and stays silent on a fresh repo, where there is nothing to conflict
// with and the note would be noise.
func TestDoctorRender_DoubleFireNote(t *testing.T) {
	installed := New(filepath.Join(t.TempDir(), ".claude", "settings.json"))
	if err := installed.Install(context.Background(), "bough hook handle"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	report, err := installed.Doctor(context.Background(), "")
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	var withHooks strings.Builder
	report.Render(&withHooks)
	if !strings.Contains(withHooks.String(), "double-fire") {
		t.Errorf("expected the double-fire note when bough hooks are wired:\n%s", withHooks.String())
	}
	// With no plugin enabled here, bough must not claim a conflict it cannot
	// see — it points at the command that shows the other scope instead.
	if strings.Contains(withHooks.String(), "WARNING") {
		t.Errorf("doctor warns of a double-fire with no plugin enabled:\n%s", withHooks.String())
	}
	for _, want := range []string{"bough-hooks", "bough-all", "claude plugin list"} {
		if !strings.Contains(withHooks.String(), want) {
			t.Errorf("note does not mention %q:\n%s", want, withHooks.String())
		}
	}
	// The v0.17.0 wording claimed bough's hooks live ONLY in settings.json.
	// The plugins ship them again, so that sentence must not come back.
	if strings.Contains(withHooks.String(), "live only here") {
		t.Errorf("doctor still claims hooks live only in settings.json:\n%s", withHooks.String())
	}

	// fresh repo (no bough hooks) -> note absent
	fresh := New(filepath.Join(t.TempDir(), ".claude", "settings.json"))
	freshReport, err := fresh.Doctor(context.Background(), "")
	if err != nil {
		t.Fatalf("Doctor(fresh): %v", err)
	}
	var noHooks strings.Builder
	freshReport.Render(&noHooks)
	if strings.Contains(noHooks.String(), "double-fire") {
		t.Errorf("did not expect the double-fire note on a fresh repo:\n%s", noHooks.String())
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

// TestEnabledHookPlugins covers the read side: which enabledPlugins entries
// count as a hook-bearing bough variant. `bough` must not — it ships commands
// and a skill only, so flagging it would cry wolf on the one variant that is
// safe to install anywhere.
func TestEnabledHookPlugins(t *testing.T) {
	for _, tc := range []struct {
		name string
		json string
		want []string
	}{
		{"no enabledPlugins key", `{}`, nil},
		{"hook-bearing variant", `{"enabledPlugins":{"bough-all@bough":true}}`, []string{"bough-all@bough"}},
		{"both variants, sorted", `{"enabledPlugins":{"bough-hooks@mp":true,"bough-all@bough":true}}`,
			[]string{"bough-all@bough", "bough-hooks@mp"}},
		{"commands-only variant is not a conflict", `{"enabledPlugins":{"bough@bough":true}}`, nil},
		{"unrelated plugins ignored", `{"enabledPlugins":{"something@else":true}}`, nil},
		{"disabled entry ignored", `{"enabledPlugins":{"bough-all@bough":false}}`, nil},
		{"marketplace half is whatever the operator named it",
			`{"enabledPlugins":{"bough-all@my-fork":true}}`, []string{"bough-all@my-fork"}},
		{"a shape bough does not recognise is not bough's to report on",
			`{"enabledPlugins":["bough-all@bough"]}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.json), &raw); err != nil {
				t.Fatal(err)
			}
			got := enabledHookPlugins(raw)
			if !slices.Equal(got, tc.want) {
				t.Errorf("enabledHookPlugins() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDoctorRender_PluginConflictIsDetected is the payoff: with both wirings
// present in one settings.json, doctor stops hedging and states the conflict.
// This is the case the prose could only ask the operator to check by hand.
func TestDoctorRender_PluginConflictIsDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	m := New(path)
	if err := m.Install(context.Background(), "bough hook handle"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// Enable the plugin the way `claude plugin install -s project` does.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	settings["enabledPlugins"] = json.RawMessage(`{"bough-all@bough":true}`)
	out, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := m.Doctor(context.Background(), "")
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if !slices.Equal(report.HookPlugins, []string{"bough-all@bough"}) {
		t.Fatalf("HookPlugins = %v, want [bough-all@bough]", report.HookPlugins)
	}

	var sb strings.Builder
	report.Render(&sb)
	got := sb.String()
	if !strings.Contains(got, "WARNING") {
		t.Errorf("both wirings present but no warning:\n%s", got)
	}
	// The warning has to name the offender and both ways out, or the operator
	// still has to go figure out what to do.
	for _, want := range []string{"bough-all@bough", "bough claude hook uninstall", "claude plugin uninstall"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning does not mention %q:\n%s", want, got)
		}
	}
}

// TestDoctorRender_ConflictListsEveryPlugin is the follow-on to the warning
// above: when two hook-bearing plugins are enabled at once, the fix has to name
// both. Printing only the first tells the operator to run a command that leaves
// the other still firing, and the doctor said the conflict was resolved.
func TestDoctorRender_ConflictListsEveryPlugin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	m := New(path)
	if err := m.Install(context.Background(), "bough hook handle"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	settings["enabledPlugins"] = json.RawMessage(`{"bough-all@bough":true,"bough-hooks@bough":true}`)
	out, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := m.Doctor(context.Background(), "")
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	var sb strings.Builder
	report.Render(&sb)
	got := sb.String()

	// Every offender needs its own uninstall line — not just the headline.
	for _, p := range []string{"bough-all@bough", "bough-hooks@bough"} {
		if !strings.Contains(got, "claude plugin uninstall "+p) {
			t.Errorf("no uninstall line for %q; following the fix as printed would leave it firing:\n%s", p, got)
		}
	}
}

// TestDoctorRender_PluginOnlyIsNotAConflict covers the third branch: the plugin
// supplies the hooks and settings.json is empty. That is a correct setup, so it
// must not warn — but silence would read as "bough is not observing me" and
// invite the install that WOULD double-fire.
func TestDoctorRender_PluginOnlyIsNotAConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"enabledPlugins":{"bough-hooks@bough":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := New(path).Doctor(context.Background(), "")
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	var sb strings.Builder
	report.Render(&sb)
	got := sb.String()

	if strings.Contains(got, "WARNING") {
		t.Errorf("plugin-only wiring is correct; it must not warn:\n%s", got)
	}
	if !strings.Contains(got, "bough-hooks@bough") {
		t.Errorf("report does not say where the hooks come from:\n%s", got)
	}
	if !strings.Contains(got, "hook install") {
		t.Errorf("report does not warn against adding the second wiring:\n%s", got)
	}
}

// TestDoctorRender_SectionRollup pins the flutter-doctor rollup: a correctly
// wired repo with no conflict is [✓], a real double-fire is [✗]. The cross-
// scope caveat must NOT downgrade a correct setup — that was the nagging-yellow
// the redesign set out to avoid.
func TestDoctorRender_SectionRollup(t *testing.T) {
	// Clean single wiring, no plugin: section [✓], caveat present but neutral.
	clean := New(filepath.Join(t.TempDir(), ".claude", "settings.json"))
	if err := clean.Install(context.Background(), "bough hook handle"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	report, err := clean.Doctor(context.Background(), "")
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	var sb strings.Builder
	report.Render(&sb)
	got := sb.String()
	if !strings.Contains(got, "[✓] Hook wiring") {
		t.Errorf("clean wiring should roll up to [✓]:\n%s", got)
	}
	if strings.Contains(got, "[✗] Hook wiring") || strings.Contains(got, "[!] Hook wiring") {
		t.Errorf("clean wiring must not show an alarm marker:\n%s", got)
	}
	// The Observer/Cost sections carry their own markers too.
	if !strings.Contains(got, "] Observer") || !strings.Contains(got, "] Cost meter") {
		t.Errorf("Observer/Cost sections lost their [x] headers:\n%s", got)
	}

	// Add a hook-bearing plugin on top → real double-fire → [✗].
	report.HookPlugins = []string{"bough-all@bough"}
	var conflict strings.Builder
	report.Render(&conflict)
	if !strings.Contains(conflict.String(), "[✗] Hook wiring") {
		t.Errorf("a live double-fire should roll up to [✗]:\n%s", conflict.String())
	}
}
