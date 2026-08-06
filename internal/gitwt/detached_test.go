package gitwt

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestSelfResolvingWorkTreeRejectsAPlainContainer is the loud half of the
// predicate, and it is the failure that actually shipped: a plain
// directory inside a repository looks materialised, holds everything the
// operator asked for, and still resolves to the repository ABOVE it — so
// a host that isolates sessions refuses it. Asserting only the passing
// direction would keep a predicate that answers "yes" to everything.
func TestSelfResolvingWorkTreeRejectsAPlainContainer(t *testing.T) {
	root := initBareRepo(t)
	container := filepath.Join(root, "worktrees", "F-Plain")
	if err := os.MkdirAll(container, 0o755); err != nil {
		t.Fatalf("mkdir container: %v", err)
	}
	if SelfResolvingWorkTree(container) {
		t.Error("a plain directory inside a repo must not pass as an isolated work tree")
	}
}

// TestAddDetachedMakesTheContainerResolveToItself is the quiet half: the
// same path, materialised through AddDetached, must satisfy the predicate
// — and must do so WITHOUT cutting a branch, since a branch per worktree
// would accumulate under a teardown policy that deliberately keeps them.
func TestAddDetachedMakesTheContainerResolveToItself(t *testing.T) {
	root := initBareRepo(t)
	container := filepath.Join(root, "worktrees", "F-Iso")

	if err := NewRunner().AddDetached(context.Background(), root, container); err != nil {
		t.Fatalf("AddDetached: %v", err)
	}
	if !SelfResolvingWorkTree(container) {
		t.Fatal("AddDetached produced a container that still resolves elsewhere")
	}

	wts, err := NewRunner().List(context.Background(), root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, wt := range wts {
		if filepath.Base(wt.Path) == "F-Iso" && wt.Branch != "" {
			t.Errorf("container was branched (%q); it must be detached", wt.Branch)
		}
	}
}

// TestAddDetachedIsIdempotent covers the contract the hook depends on:
// Claude Code re-fires WorktreeCreate on every `--resume`, so a second
// call must be a no-op rather than an error or a teardown.
func TestAddDetachedIsIdempotent(t *testing.T) {
	root := initBareRepo(t)
	container := filepath.Join(root, "worktrees", "F-Twice")
	r := NewRunner()
	if err := r.AddDetached(context.Background(), root, container); err != nil {
		t.Fatalf("first AddDetached: %v", err)
	}
	if err := r.AddDetached(context.Background(), root, container); err != nil {
		t.Fatalf("second AddDetached must be a no-op, got: %v", err)
	}
}

// TestAddDetachedLeavesAPopulatedDirAlone protects containers created
// before this existed. They are plain directories full of sub-repo
// worktrees, possibly with a session running in them; `git worktree add`
// cannot adopt a populated directory anyway, so the only two honest
// outcomes are "refuse" and "destroy". It must refuse — and the caller
// degrades to the legacy behaviour rather than unwinding the create.
func TestAddDetachedLeavesAPopulatedDirAlone(t *testing.T) {
	root := initBareRepo(t)
	container := filepath.Join(root, "worktrees", "F-Legacy")
	if err := os.MkdirAll(container, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	keep := filepath.Join(container, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := NewRunner().AddDetached(context.Background(), root, container); err == nil {
		t.Error("AddDetached must refuse a populated container instead of adopting it")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("existing content was destroyed: %v", err)
	}
}
