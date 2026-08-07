package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/gitwt"
)

// TestWorktreeCreateHealsALegacyContainer drives the real WorktreeCreate
// hook against the exact state that reached an operator's terminal: a
// populated pre-v0.22.0 container the host refuses — and then, worse,
// keeps the session running WITHOUT isolation. The hook fires on every
// `--resume`, so it must hand back a container that already satisfies the
// host's predicate, with the legacy contents still in place.
func TestWorktreeCreateHealsALegacyContainer(t *testing.T) {
	root := t.TempDir()
	gitInitMain(t, root)
	gitInitMain(t, filepath.Join(root, "demo"))
	writeMinimalBoughYAML(t, root)

	container := filepath.Join(root, "worktrees", "F-Old")
	if err := os.MkdirAll(container, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	keep := filepath.Join(container, "keep.txt")
	if err := os.WriteFile(keep, []byte("legacy\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := worktreeCreateHook(t, root, "F-Old")

	if !gitwt.NewRunner().SelfResolvingWorkTree(context.Background(), got) {
		t.Fatal("hook returned a container the host still refuses — the legacy shape was not healed")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("legacy content lost in the heal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(got, "demo", ".git")); err != nil {
		t.Errorf("sub-repo worktree not materialised after the heal: %v", err)
	}
}

// TestRunRepairConvertsTheFleet covers the command path: mixed fleet in,
// only the legacy container converted, the already-isolated one untouched,
// and the summary states both counts.
func TestRunRepairConvertsTheFleet(t *testing.T) {
	root := t.TempDir()
	gitInitMain(t, root)

	legacy := filepath.Join(root, "worktrees", "F-Legacy")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	keep := filepath.Join(legacy, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runner := gitwt.NewRunner()
	if err := runner.AddDetached(context.Background(), root, filepath.Join(root, "worktrees", "F-New")); err != nil {
		t.Fatalf("AddDetached: %v", err)
	}

	var errBuf bytes.Buffer
	if err := runRepair(context.Background(), &errBuf, root, false); err != nil {
		t.Fatalf("runRepair: %v\n%s", err, errBuf.String())
	}
	if !runner.SelfResolvingWorkTree(context.Background(), legacy) {
		t.Error("legacy container was not converted")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("content lost: %v", err)
	}
	if !strings.Contains(errBuf.String(), "already isolated: 1 / converted: 1 / failed: 0") {
		t.Errorf("summary must state both counts, got:\n%s", errBuf.String())
	}
}

// TestRunRepairDryRunChangesNothing pins that --dry-run is a report, not a
// smaller apply: the legacy container must still resolve to the monorepo
// root afterwards.
func TestRunRepairDryRunChangesNothing(t *testing.T) {
	root := t.TempDir()
	gitInitMain(t, root)
	legacy := filepath.Join(root, "worktrees", "F-Legacy")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var errBuf bytes.Buffer
	if err := runRepair(context.Background(), &errBuf, root, true); err != nil {
		t.Fatalf("runRepair --dry-run: %v", err)
	}
	if gitwt.NewRunner().SelfResolvingWorkTree(context.Background(), legacy) {
		t.Error("--dry-run converted a container; it must only report")
	}
	if !strings.Contains(errBuf.String(), "would convert F-Legacy") {
		t.Errorf("dry-run must name what it would convert, got:\n%s", errBuf.String())
	}
}

// TestRepairCmdNeedsNoConfig drives the COMMAND, not runRepair, because
// that is where the defect was: the RunE loaded .bough.yaml and exited 1
// without one, while `bough doctor` — which tells the operator to run
// repair — needs no config. Every unit test here called runRepair
// directly and sailed past the wiring, so the binary failed where the
// suite was green.
func TestRepairCmdNeedsNoConfig(t *testing.T) {
	root := t.TempDir() // no .bough.yaml anywhere
	gitInitMain(t, root)
	legacy := filepath.Join(root, "worktrees", "F-NoConfig")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	cmd := newRepairCmd()
	cmd.SetArgs(nil)
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("repair must run without a config file: %v\nstderr:\n%s", err, errBuf.String())
	}
	if !gitwt.NewRunner().SelfResolvingWorkTree(context.Background(), legacy) {
		t.Errorf("container not converted:\n%s", errBuf.String())
	}
}

// TestRunRepairOutsideARepoIsANoOp is the same boundary the create path
// keeps: outside a git repo nothing above a container can capture git's
// resolution, so a plain directory is the CORRECT shape and repair must
// not manufacture a repo to "fix" it.
func TestRunRepairOutsideARepoIsANoOp(t *testing.T) {
	root := t.TempDir() // deliberately NOT a git repo
	plain := filepath.Join(root, "worktrees", "F-Plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var errBuf bytes.Buffer
	if err := runRepair(context.Background(), &errBuf, root, false); err != nil {
		t.Fatalf("runRepair outside a repo must not fail: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plain, ".git")); !os.IsNotExist(err) {
		t.Errorf("repair manufactured git state outside a repo (err=%v)", err)
	}
}
