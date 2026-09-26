// Package hooks is the real-binary end-to-end test for the hook
// lifecycle. It builds the actual bough binary, then drives it through
// install → handle → doctor → uninstall against a tmpdir-rooted
// .claude/settings.json. The unit tests in internal/hooks pin the
// per-method behaviour; this suite proves the chain works as a real CLI
// user would invoke it.
//
// Round 5 review insistence: hook auto-wire without a real-binary
// integration check is exactly how regressions ship. The subprocess
// approach (versus an in-process call) is the same pattern the other
// conformance suites use for production stdio paths.
package hooks_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHooks_EndToEnd_InstallHandleDoctorUninstall walks the canonical
// user flow: install the wiring, drive retired and unknown events
// through `bough hook handle`, render the doctor report, prune a
// v0.27.0 settings.json, then uninstall. The two worktree events need a
// monorepo to act on, so they are driven where one exists:
// internal/cli/worktree_hook_test.go and scripts/entrypoint-smoke.sh.
func TestHooks_EndToEnd_InstallHandleDoctorUninstall(t *testing.T) {
	bin := buildBoughBinary(t)
	workdir := t.TempDir()

	run := func(t *testing.T, label string, stdin string, args ...string) (string, string) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = workdir
		cmd.Env = os.Environ()
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s failed: %v\nstdout: %s\nstderr: %s", label, err, stdout.String(), stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	// install
	run(t, "hook install", "", "hook", "install")
	settingsPath := filepath.Join(workdir, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("settings.json not created at %s: %v", settingsPath, err)
	}

	// list shows the two events bough handles, and nothing else.
	stdout, _ := run(t, "hook list", "", "hook", "list")
	for _, event := range []string{"WorktreeCreate", "WorktreeRemove"} {
		if !strings.Contains(stdout, event) {
			t.Errorf("hook list missing %s: %s", event, stdout)
		}
	}
	for _, retired := range []string{"PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop", "SessionEnd", "PreCompact"} {
		if strings.Contains(stdout, retired) {
			t.Errorf("hook list still wires the retired event %s: %s", retired, stdout)
		}
	}

	// A retired event exits 0 and writes nothing to stdout. This is what
	// keeps an un-updated settings.json or a cached plugin manifest from
	// failing every tool call until the operator re-runs install.
	for _, retired := range []string{"PreToolUse", "SessionEnd"} {
		out, errOut := run(t, "hook handle "+retired,
			`{"hook_event_name":"`+retired+`","tool_name":"Edit"}`,
			"hook", "handle", "--event", retired)
		if strings.TrimSpace(out) != "" {
			t.Errorf("a retired event must print nothing to stdout (it is folded into the model's context), got: %q", out)
		}
		if !strings.Contains(errOut, "retired") {
			t.Errorf("a retired event should say so on stderr, got: %q", errOut)
		}
	}

	// An event that is neither wired nor retired is an error. Exiting 0
	// here is what a host reports as "hook succeeded but returned no
	// worktree path", with nothing naming the typo.
	bogus := exec.Command(bin, "hook", "handle", "--event", "WorktreeCreat")
	bogus.Dir = workdir
	bogus.Stdin = strings.NewReader("{}")
	if err := bogus.Run(); err == nil {
		t.Error("a misspelled --event must fail rather than exit 0 with empty stdout")
	}

	// doctor reports the wired state.
	stdout, _ = run(t, "doctor", "", "doctor")
	if !strings.Contains(stdout, "Hook wiring") || !strings.Contains(stdout, "2/2 wired") {
		t.Errorf("doctor missing the wired-state header: %s", stdout)
	}

	// The upgrade path an operator actually walks: a settings.json left
	// over from v0.27.0 wires all eight events. One install must leave
	// exactly the two that do something. Seeded with the full set rather
	// than a sample, so a retired event that stops being pruned cannot
	// hide in the half the fixture skipped.
	staleEvents := []string{
		"PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop", "SessionEnd", "PreCompact",
		"WorktreeCreate", "WorktreeRemove",
	}
	var stale strings.Builder
	stale.WriteString(`{"hooks":{`)
	for i, e := range staleEvents {
		if i > 0 {
			stale.WriteString(",")
		}
		fmt.Fprintf(&stale, `%q:[{"hooks":[{"type":"command","command":"bough hook handle --event %s"}]}]`, e, e)
	}
	stale.WriteString("}}")
	if err := os.WriteFile(settingsPath, []byte(stale.String()), 0o644); err != nil {
		t.Fatalf("seed stale settings.json: %v", err)
	}
	listOut, _ := run(t, "hook list on stale wiring", "", "hook", "list")
	doctorOut, _ := run(t, "doctor on stale wiring", "", "doctor")
	for _, e := range staleEvents[:6] { // the six retired ones
		if !strings.Contains(listOut, e) {
			t.Errorf("hook list should show stale retired %s until install prunes it: %s", e, listOut)
		}
		if !strings.Contains(doctorOut, e) {
			t.Errorf("doctor should name the stale retired %s: %s", e, doctorOut)
		}
	}
	run(t, "hook install over stale wiring", "", "hook", "install")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var doc struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	if len(doc.Hooks) != 2 {
		t.Errorf("install should leave exactly the two wired events, got %d: %s", len(doc.Hooks), data)
	}
	for _, retired := range staleEvents[:6] {
		if _, ok := doc.Hooks[retired]; ok {
			t.Errorf("%s survived install (an empty array counts — the key must be gone): %s", retired, data)
		}
	}

	// uninstall + list back to empty
	run(t, "hook uninstall", "", "hook", "uninstall")
	stdout, _ = run(t, "hook list after uninstall", "", "hook", "list")
	if !strings.Contains(stdout, "(no hooks wired") {
		t.Errorf("post-uninstall list should report empty: %s", stdout)
	}
	data, err = os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json after uninstall: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	if _, ok := parsed["hooks"]; ok {
		t.Errorf("hooks key should be removed after uninstall: %s", data)
	}
}

// buildBoughBinary compiles the actual cmd/bough binary into a
// throwaway directory so the integration test exercises the real
// CLI surface end-to-end. The build cost is paid once per test
// run and amortised across the per-step assertions.
func buildBoughBinary(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bough-hooks-conf-*")
	if err != nil {
		t.Fatalf("mktempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	bin := filepath.Join(dir, "bough")
	repoRoot := findRepoRoot(t)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/bough")
	cmd.Dir = repoRoot
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/bough: %v\n%s", err, out)
	}
	return bin
}

// findRepoRoot resolves the bough module root via `go env GOMOD`
// so the test still works when invoked from any nested package.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" {
		t.Fatalf("go env GOMOD empty — not in a module")
	}
	return filepath.Dir(mod)
}
