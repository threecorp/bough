package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/hooks"
)

// gitInitMain materialises a minimal git repo with a `main` branch and
// one commit, so create's AddOrAttach can branch off it without a clone
// or a network fetch.
func gitInitMain(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("commit", "--allow-empty", "-m", "init")
}

// TestHookHandle_WorktreeCreateEmitsPath is the regression for the
// dogfood bug: `bough hook install` wires WorktreeCreate →
// `bough hook handle --event WorktreeCreate`, but the handler's switch
// had no WorktreeCreate case, so it returned exit 0 with an empty stdout
// and Claude Code aborted with "hook succeeded but returned no worktree
// path" — and no worktree was created. The unified handler must run the
// create pipeline and print the worktree root path Claude Code cd's into.
func TestHookHandle_WorktreeCreateEmitsPath(t *testing.T) {
	root := t.TempDir()
	// A present local git repo so AddOrAttach branches off it without a
	// clone; no engines declared, so create stays docker/nix-free + fast.
	gitInitMain(t, filepath.Join(root, "demo"))
	yaml := "schema_version: 2\n" +
		"monorepo_root: \".\"\n" +
		"repositories:\n" +
		"  - name: demo\n" +
		"    branch_strategy: main\n" +
		"registry:\n" +
		"  path: \".bough-ports.json\"\n"
	if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write .bough.yaml: %v", err)
	}

	cmd := newHookHandleCmd()
	cmd.SetArgs([]string{"--event", "WorktreeCreate", "--out", filepath.Join(root, "obs.jsonl")})
	cmd.SetIn(strings.NewReader(fmt.Sprintf(`{"name":"F-Test","cwd":%q}`, root)))
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("hook handle WorktreeCreate: %v\nstderr:\n%s", err, errBuf.String())
	}

	got := strings.TrimSpace(out.String())
	want := filepath.Join(root, ".worktrees", "F-Test")
	if got != want {
		t.Errorf("stdout worktree path = %q, want %q\nstderr:\n%s", got, want, errBuf.String())
	}
	if _, err := os.Stat(filepath.Join(want, "demo", ".git")); err != nil {
		t.Errorf("worktree repo not materialised: %v", err)
	}
}

// TestHookHandle_WorktreeCreateMissingName surfaces a payload with no
// worktree name as a hook failure, rather than the old silent
// empty-stdout success.
func TestHookHandle_WorktreeCreateMissingName(t *testing.T) {
	cmd := newHookHandleCmd()
	cmd.SetArgs([]string{"--event", "WorktreeCreate", "--out", filepath.Join(t.TempDir(), "obs.jsonl")})
	cmd.SetIn(strings.NewReader(`{"cwd":"/tmp"}`))
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	if err := cmd.Execute(); err == nil {
		t.Errorf("expected error for WorktreeCreate payload with no name, got nil (stdout=%q)", out.String())
	}
}

// TestHookHandle_AllEventsRecordObservation is the whole-surface check:
// every event `bough hook install` wires must append exactly one
// observation tagged with the event name and exit cleanly. WorktreeCreate
// / WorktreeRemove are exercised separately below (they need a repo);
// the remaining six carry no repo-mutating dispatch, so a bare .bough.yaml-
// less run is safe.
func TestHookHandle_AllEventsRecordObservation(t *testing.T) {
	for _, ev := range []hooks.HookEvent{
		hooks.EventPreToolUse,
		hooks.EventPostToolUse,
		hooks.EventUserPromptSubmit,
		hooks.EventStop,
		hooks.EventSessionEnd,
		hooks.EventPreCompact,
	} {
		t.Run(string(ev), func(t *testing.T) {
			obs := filepath.Join(t.TempDir(), "obs.jsonl")
			cmd := newHookHandleCmd()
			cmd.SetArgs([]string{"--event", string(ev), "--out", obs})
			cmd.SetIn(strings.NewReader(`{"prompt":"x","tool_name":"Bash","session_id":"s"}`))
			var out, errBuf bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errBuf)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("hook handle %s: %v\n%s", ev, err, errBuf.String())
			}
			data, err := os.ReadFile(obs)
			if err != nil {
				t.Fatalf("read obs: %v", err)
			}
			if !bytes.Contains(data, []byte(fmt.Sprintf(`"event":%q`, string(ev)))) {
				t.Errorf("%s: no observation tagged with the event was recorded:\n%s", ev, data)
			}
		})
	}
}

// TestHookHandle_WorktreeRemoveTearsDown is the WorktreeRemove twin of
// the WorktreeCreate test: after a create, the WorktreeRemove hook must
// tear the worktree tree back down.
func TestHookHandle_WorktreeRemoveTearsDown(t *testing.T) {
	root := t.TempDir()
	gitInitMain(t, filepath.Join(root, "demo"))
	yaml := "schema_version: 2\n" +
		"monorepo_root: \".\"\n" +
		"repositories:\n" +
		"  - name: demo\n" +
		"    branch_strategy: main\n" +
		"registry:\n" +
		"  path: \".bough-ports.json\"\n"
	if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write .bough.yaml: %v", err)
	}
	obs := filepath.Join(root, "obs.jsonl")

	handle := func(event, payload string) {
		t.Helper()
		cmd := newHookHandleCmd()
		cmd.SetArgs([]string{"--event", event, "--out", obs})
		cmd.SetIn(strings.NewReader(payload))
		var out, errBuf bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errBuf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("hook handle %s: %v\n%s", event, err, errBuf.String())
		}
	}

	handle("WorktreeCreate", fmt.Sprintf(`{"name":"F-Rm","cwd":%q}`, root))
	wt := filepath.Join(root, ".worktrees", "F-Rm")
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree not created by WorktreeCreate: %v", err)
	}

	handle("WorktreeRemove", fmt.Sprintf(`{"worktree_path":%q}`, wt))
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree still present after WorktreeRemove: err=%v", err)
	}
}
