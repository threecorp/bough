package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// TestRepoNameFromCwd covers the retrospective-review fix for PR #25's
// buildMatchContext: it never populated qualitygate.MatchContext.Repo, so
// a gate's on_repo matcher compared against an always-empty string and
// could never match. repoNameFromCwd derives that value from cwd relative
// to the resolved monorepo root, mirroring resolveMonorepoRoot's own
// worktree-segment handling.
func TestRepoNameFromCwd(t *testing.T) {
	// rootRel / cwdRel are both expressed relative to a shared tempdir
	// "base", so "cwd outside root" can land as root's sibling rather
	// than its descendant while every case still resolves under one
	// real, existing directory tree.
	tests := []struct {
		name    string
		rootRel string
		cwdRel  string
		want    string
	}{
		{
			name:    "plain sub-repo directly under root",
			rootRel: "root",
			cwdRel:  "root/extremo-api/pkg/foo",
			want:    "extremo-api",
		},
		{
			name:    "cwd is the monorepo root itself",
			rootRel: "root",
			cwdRel:  "root",
			want:    "",
		},
		{
			name:    "worktrees/ layout: repo is the segment after the branch name",
			rootRel: "root",
			cwdRel:  "root/" + worktreesName + "/F-feat/extremo-api/server",
			want:    "extremo-api",
		},
		{
			name:    "legacy .worktrees/ layout",
			rootRel: "root",
			cwdRel:  "root/" + legacyWtName + "/F-feat/extremo-view",
			want:    "extremo-view",
		},
		{
			name:    "worktrees/ layout too shallow (branch dir itself, no repo segment)",
			rootRel: "root",
			cwdRel:  "root/" + worktreesName + "/F-feat",
			want:    "",
		},
		{
			name:    "cwd outside root",
			rootRel: "root",
			cwdRel:  "unrelated-dir",
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, filepath.FromSlash(tt.rootRel))
			cwd := filepath.Join(base, filepath.FromSlash(tt.cwdRel))
			if got := repoNameFromCwd(cwd, root); got != tt.want {
				t.Errorf("repoNameFromCwd(%q, %q) = %q, want %q", cwd, root, got, tt.want)
			}
		})
	}
}

// boughYAMLFixture renders a minimal valid .bough.yaml with one
// quality_gates entry, matching the shape config_test.go's
// TestLoad_acceptsQualityGates pins.
func boughYAMLFixture(gateName, onEvent, onRepo, command string) string {
	onRepoLine := ""
	if onRepo != "" {
		onRepoLine = fmt.Sprintf("    on_repo: %s\n", onRepo)
	}
	return fmt.Sprintf(`schema_version: 2
monorepo_root: "."
repositories:
  - name: demo
    branch_strategy: develop
registry:
  path: .bough-ports.json
quality_gates:
  - name: %s
    command: "%s"
    on_event: %s
%s`, gateName, command, onEvent, onRepoLine)
}

// TestDispatchQualityGates_ResolvesConfigFromSubRepoCwd is the regression
// test for PR #25's dispatchQualityGates: it resolved cfgPath as a bare
// cwd-relative ".bough.yaml" (or $BOUGH_CONFIG), so a `bough hook handle`
// invocation whose cwd sits inside a sub-repo (the ordinary case per this
// file's own ECC pooling model) never found the monorepo's canonical
// .bough.yaml and silently never ran any gate. It now resolves through
// resolveMonorepoRoot + resolveConfigPath.
func TestDispatchQualityGates_ResolvesConfigFromSubRepoCwd(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "ran.marker")
	yaml := boughYAMLFixture("touch-marker", "PostToolUse", "", "touch "+marker)
	if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write .bough.yaml: %v", err)
	}
	subRepoDir := filepath.Join(root, "extremo-api", "pkg")
	if err := os.MkdirAll(subRepoDir, 0o755); err != nil {
		t.Fatalf("mkdir subrepo: %v", err)
	}

	prev, _ := os.Getwd()
	defer func() { _ = os.Chdir(prev) }()
	if err := os.Chdir(subRepoDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&stderr)

	payload := []byte(`{"tool_name":"Edit","tool_input":{"file_path":"x.go"}}`)
	dispatchQualityGates(cmd, "PostToolUse", payload)

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("gate did not run from sub-repo cwd (marker missing): %v\nstderr: %s", err, stderr.String())
	}
}

// TestDispatchQualityGates_OnRepoMatchesDerivedRepo proves on_repo actually
// filters: a gate scoped to the repo cwd resolves to must run, and the same
// gate scoped to a different repo name must not.
func TestDispatchQualityGates_OnRepoMatchesDerivedRepo(t *testing.T) {
	run := func(t *testing.T, onRepo string) bool {
		t.Helper()
		root := t.TempDir()
		marker := filepath.Join(root, "ran.marker")
		yaml := boughYAMLFixture("scoped-gate", "PostToolUse", onRepo, "touch "+marker)
		if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatalf("write .bough.yaml: %v", err)
		}
		cwd := filepath.Join(root, "extremo-api")
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		prev, _ := os.Getwd()
		defer func() { _ = os.Chdir(prev) }()
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("chdir: %v", err)
		}
		cmd := &cobra.Command{}
		cmd.SetErr(&bytes.Buffer{})
		dispatchQualityGates(cmd, "PostToolUse", []byte(`{"tool_name":"Bash"}`))
		_, err := os.Stat(marker)
		return err == nil
	}

	if !run(t, "extremo-api") {
		t.Error("on_repo: extremo-api should match cwd under extremo-api/, gate did not run")
	}
	if run(t, "extremo-view") {
		t.Error("on_repo: extremo-view should NOT match cwd under extremo-api/, but the gate ran")
	}
}

// TestBuildMatchContext_CarriesRepo pins that buildMatchContext threads its
// repo argument into MatchContext.Repo (previously always the zero value).
func TestBuildMatchContext_CarriesRepo(t *testing.T) {
	mc := buildMatchContext("PostToolUse", []byte(`{"tool_name":"Edit"}`), "extremo-api")
	if mc.Repo != "extremo-api" {
		t.Errorf("Repo = %q, want %q", mc.Repo, "extremo-api")
	}
	if mc.Tool != "Edit" {
		t.Errorf("Tool = %q, want %q", mc.Tool, "Edit")
	}
}
