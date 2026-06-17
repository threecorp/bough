package gitwt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initBareRepo materialises a self-contained git repo in t.TempDir with
// one commit on `main`, then sets origin/HEAD so DetectBase has
// something to read back. The repo is bare-shaped: no working tree, just
// .git, mirroring the production "src repo is a checkout of the
// monorepo" assumption.
func initBareRepo(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = src
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run("git", "init", "-b", "main")
	run("git", "commit", "--allow-empty", "-m", "init")
	// origin/HEAD needs an upstream — we cheat by adding self as origin
	// and copying the local branch ref to origin/main so DetectBase has
	// a refs/remotes/origin/main to parse.
	run("git", "remote", "add", "origin", src)
	// Make refs/remotes/origin/HEAD point at refs/remotes/origin/main.
	// `git fetch` against self would be cleaner but slower; the manual
	// ref copy keeps the test fast.
	mainRev, err := exec.Command("git", "-C", src, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	rev := strings.TrimSpace(string(mainRev))
	if err := os.MkdirAll(filepath.Join(src, ".git", "refs", "remotes", "origin"), 0o755); err != nil {
		t.Fatalf("mkdir refs/remotes/origin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, ".git", "refs", "remotes", "origin", "main"), []byte(rev+"\n"), 0o644); err != nil {
		t.Fatalf("write origin/main: %v", err)
	}
	run("git", "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return src
}

func TestRunner_DetectBase_fromOriginHEAD(t *testing.T) {
	src := initBareRepo(t)
	r := NewRunner()
	base, err := r.DetectBase(context.Background(), src, "")
	if err != nil {
		t.Fatalf("DetectBase: %v", err)
	}
	if base != "main" {
		t.Errorf("got %q want %q", base, "main")
	}
}

func TestRunner_DetectBase_fallbackOnMissing(t *testing.T) {
	// A repo without origin/HEAD set: DetectBase must return the fallback.
	src := t.TempDir()
	if out, err := exec.Command("git", "-C", src, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	r := NewRunner()
	base, err := r.DetectBase(context.Background(), src, "develop")
	if err != nil {
		t.Fatalf("DetectBase: %v", err)
	}
	if base != "develop" {
		t.Errorf("got %q want %q (fallback)", base, "develop")
	}
}

func TestRunner_AddOrAttach_createsNewBranch(t *testing.T) {
	src := initBareRepo(t)
	dst := filepath.Join(t.TempDir(), "wt")
	r := NewRunner()
	created, err := r.AddOrAttach(context.Background(), src, dst, "F-Feat", "main")
	if err != nil {
		t.Fatalf("AddOrAttach: %v", err)
	}
	if !created {
		t.Errorf("expected created=true on fresh branch")
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err != nil {
		t.Errorf(".git missing in worktree dst: %v", err)
	}
}

// TestRunner_AddOrAttach_fetchUsesOriginBase exercises the Fetch=true
// path that bough's create command flips on so a stale local checkout
// does not silently seed the worktree from N-commit-old refs. The
// origin remote is `src` itself (set by initBareRepo), so the fetch
// step succeeds and `worktree add` uses `origin/main` as the base.
func TestRunner_AddOrAttach_fetchUsesOriginBase(t *testing.T) {
	src := initBareRepo(t)
	dst := filepath.Join(t.TempDir(), "wt")
	r := NewRunner()
	r.Fetch = true
	created, err := r.AddOrAttach(context.Background(), src, dst, "F-Fetch", "main")
	if err != nil {
		t.Fatalf("AddOrAttach with Fetch=true: %v", err)
	}
	if !created {
		t.Errorf("expected created=true on fresh branch")
	}
	sha, err := r.HeadSHA(context.Background(), dst)
	if err != nil || sha == "" {
		t.Errorf("HeadSHA after fetched create: sha=%q err=%v", sha, err)
	}
}

func TestRunner_AddOrAttach_attachExistingBranch(t *testing.T) {
	src := initBareRepo(t)
	r := NewRunner()
	// Create the branch first (without a worktree).
	if out, err := exec.Command("git", "-C", src, "branch", "F-Existing").CombinedOutput(); err != nil {
		t.Fatalf("pre-create branch: %v\n%s", err, out)
	}
	dst := filepath.Join(t.TempDir(), "wt")
	created, err := r.AddOrAttach(context.Background(), src, dst, "F-Existing", "main")
	if err != nil {
		t.Fatalf("AddOrAttach: %v", err)
	}
	if created {
		t.Errorf("expected created=false (attach path)")
	}
}

func TestRunner_RemoveAndDeleteBranch(t *testing.T) {
	src := initBareRepo(t)
	r := NewRunner()
	dst := filepath.Join(t.TempDir(), "wt")
	if _, err := r.AddOrAttach(context.Background(), src, dst, "F-Rm", "main"); err != nil {
		t.Fatalf("setup AddOrAttach: %v", err)
	}
	if err := r.Remove(context.Background(), src, dst, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present: err=%v", err)
	}
	if err := r.DeleteBranch(context.Background(), src, "F-Rm"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	// Idempotent: repeating DeleteBranch returns nil even for "no such branch".
	if err := r.DeleteBranch(context.Background(), src, "F-Rm"); err != nil {
		t.Errorf("DeleteBranch second call should be idempotent, got %v", err)
	}
}

func TestRunner_Remove_fallbackOnPartial(t *testing.T) {
	// Mimic the "interrupted previous teardown" scenario: the worktree
	// directory was nuked out-of-band but git still has a record. Remove
	// should fall through to `git worktree prune` and not error out.
	src := initBareRepo(t)
	r := NewRunner()
	dst := filepath.Join(t.TempDir(), "wt-partial")
	if _, err := r.AddOrAttach(context.Background(), src, dst, "F-P", "main"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Out-of-band nuke before Remove runs.
	if err := os.RemoveAll(dst); err != nil {
		t.Fatalf("ob rm: %v", err)
	}
	if err := r.Remove(context.Background(), src, dst, true); err != nil {
		t.Fatalf("Remove with partial state: %v", err)
	}
}

func TestRunner_List(t *testing.T) {
	src := initBareRepo(t)
	r := NewRunner()
	dst := filepath.Join(t.TempDir(), "wt-list")
	if _, err := r.AddOrAttach(context.Background(), src, dst, "F-List", "main"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	wts, err := r.List(context.Background(), src)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// `git worktree list` returns the main checkout PLUS the new worktree.
	if got := len(wts); got < 2 {
		t.Fatalf("List: got %d entries, want >= 2", got)
	}
	var sawNew bool
	for _, w := range wts {
		if w.Branch == "F-List" {
			sawNew = true
		}
	}
	if !sawNew {
		t.Errorf("List did not include the F-List worktree: %+v", wts)
	}
}
