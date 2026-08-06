package gitwt

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
	if NewRunner().SelfResolvingWorkTree(context.Background(), container) {
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
	if !NewRunner().SelfResolvingWorkTree(context.Background(), container) {
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

// TestSelfResolvingWorkTreeAcceptsAStandaloneRepo pins the boundary the
// predicate must NOT overshoot. A container that is simply its own
// repository resolves to itself, and a host accepts it — measured: a hook
// returning such a container started the session normally. An earlier
// version of this predicate additionally required the git dir to differ
// from `<dir>/.git`, which rejected exactly this shape; `bough doctor`
// then told the operator to rebuild a worktree that worked, and gave a
// reason ("git resolves them to <root>") that was not true of it.
func TestSelfResolvingWorkTreeAcceptsAStandaloneRepo(t *testing.T) {
	root := initBareRepo(t)
	container := filepath.Join(root, "worktrees", "F-Own")
	if err := os.MkdirAll(container, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", container}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if !NewRunner().SelfResolvingWorkTree(context.Background(), container) {
		t.Error("a container that is its own repository resolves to itself; the host accepts it and so must this")
	}
}

// TestPopulatedContainerIsATypedError lets the caller tell the ordinary
// legacy state apart from a real failure. Without the distinction, create
// printed a three-line warning on every hook fire — and the host re-fires
// WorktreeCreate on each `--resume` — for a condition nothing can act on.
func TestPopulatedContainerIsATypedError(t *testing.T) {
	root := initBareRepo(t)
	container := filepath.Join(root, "worktrees", "F-Full")
	if err := os.MkdirAll(container, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(container, "x"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := NewRunner().AddDetached(context.Background(), root, container)
	if !errors.Is(err, ErrPopulatedContainer) {
		t.Errorf("want ErrPopulatedContainer, got %v", err)
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
