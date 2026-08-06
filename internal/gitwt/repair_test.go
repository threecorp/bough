package gitwt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepairInPlaceConvertsAPopulatedContainer is the operation the
// operator-facing incident needed: a pre-AddDetached container full of
// content becomes a work tree of its own with nothing inside it moved,
// deleted or checked out over.
func TestRepairInPlaceConvertsAPopulatedContainer(t *testing.T) {
	root := initBareRepo(t)
	container := filepath.Join(root, "worktrees", "F-Legacy")
	if err := os.MkdirAll(filepath.Join(container, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	keep := filepath.Join(container, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(keep, filepath.Join(container, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	r := NewRunner()
	if err := r.RepairInPlace(context.Background(), root, container); err != nil {
		t.Fatalf("RepairInPlace: %v", err)
	}
	if !r.SelfResolvingWorkTree(context.Background(), container) {
		t.Fatal("repaired container still resolves outside itself")
	}
	for _, p := range []string{keep, filepath.Join(container, "sub"), filepath.Join(container, "link")} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("content lost in repair: %v", err)
		}
	}
	// Checked out at the empty tree: nothing tracked, so nothing can show
	// as a staged deletion — only the untracked entries it already had.
	out, err := exec.Command("git", "-C", container, "status", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(line, "D ") || strings.HasPrefix(line, " D") {
			t.Errorf("repair left tracked deletions behind:\n%s", out)
		}
	}
	// No staging leftovers: the throwaway path is gone from disk and from
	// the admin records.
	if _, err := os.Stat(filepath.Join(root, ".bough-repair-tmp")); !os.IsNotExist(err) {
		t.Errorf("staging dir left behind (err=%v)", err)
	}
	listed, err := exec.Command("git", "-C", root, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if strings.Contains(string(listed), ".bough-repair-tmp") {
		t.Errorf("staging worktree entry left behind:\n%s", listed)
	}
}

// TestRepairInPlaceIsIdempotent mirrors AddDetached's contract: the hook
// self-heal runs on every `--resume`, so a second pass over an
// already-converted container must be a no-op.
func TestRepairInPlaceIsIdempotent(t *testing.T) {
	root := initBareRepo(t)
	container := filepath.Join(root, "worktrees", "F-Twice")
	if err := os.MkdirAll(container, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	r := NewRunner()
	if err := r.RepairInPlace(context.Background(), root, container); err != nil {
		t.Fatalf("first RepairInPlace: %v", err)
	}
	if err := r.RepairInPlace(context.Background(), root, container); err != nil {
		t.Fatalf("second RepairInPlace must be a no-op, got: %v", err)
	}
}

// TestRepairInPlaceRefusesToClobberAGitDir pins the conservative edge: a
// container holding a real `.git` DIRECTORY that still does not resolve
// to itself is broken in a way this procedure cannot judge, so the move
// must refuse — and the throwaway admin entry must be rolled back rather
// than surviving as a stale record in `git worktree list`.
func TestRepairInPlaceRefusesToClobberAGitDir(t *testing.T) {
	root := initBareRepo(t)
	container := filepath.Join(root, "worktrees", "F-Broken")
	if err := os.MkdirAll(filepath.Join(container, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	err := NewRunner().RepairInPlace(context.Background(), root, container)
	if err == nil {
		t.Fatal("RepairInPlace must refuse a container that owns a .git directory")
	}
	listed, listErr := exec.Command("git", "-C", root, "worktree", "list", "--porcelain").Output()
	if listErr != nil {
		t.Fatalf("worktree list: %v", listErr)
	}
	if strings.Contains(string(listed), ".bough-repair-tmp") {
		t.Errorf("failed repair left a staging worktree entry:\n%s", listed)
	}
}
