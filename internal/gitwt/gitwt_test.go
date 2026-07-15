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
	created, _, err := r.AddOrAttach(context.Background(), src, dst, "F-Feat", "main")
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
	created, effBase, err := r.AddOrAttach(context.Background(), src, dst, "F-Fetch", "main")
	if err != nil {
		t.Fatalf("AddOrAttach with Fetch=true: %v", err)
	}
	if !created {
		t.Errorf("expected created=true on fresh branch")
	}
	// The fetch against self (origin=src) succeeds, so the worktree must be
	// seeded from origin/main and AddOrAttach must report that exact ref back
	// (not the local "main") — the caller logs this verbatim.
	if effBase != "origin/main" {
		t.Errorf("effectiveBase = %q, want %q", effBase, "origin/main")
	}
	sha, err := r.HeadSHA(context.Background(), dst)
	if err != nil || sha == "" {
		t.Errorf("HeadSHA after fetched create: sha=%q err=%v", sha, err)
	}
	// --no-track guard: branching off remote-tracking origin/main must NOT
	// set the new branch's upstream, or auba's bare `git push`
	// (push.default=simple) would refuse (upstream=origin/main != origin/F-Fetch).
	up, _ := exec.Command("git", "-C", dst, "config", "--get", "branch.F-Fetch.merge").CombinedOutput()
	if strings.TrimSpace(string(up)) != "" {
		t.Errorf("new branch upstream = %q; --no-track should leave it empty", strings.TrimSpace(string(up)))
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
	created, _, err := r.AddOrAttach(context.Background(), src, dst, "F-Existing", "main")
	if err != nil {
		t.Fatalf("AddOrAttach: %v", err)
	}
	if created {
		t.Errorf("expected created=false (attach path)")
	}
}

// TestRunner_AddOrAttach_ResumeIsNoOpThroughSymlink is the regression
// guard for the #7-review finding: the resume idempotency check
// compared the caller's literal dst against git's symlink-resolved
// `worktree list --porcelain` path, so on any host where dst
// traverses a symlink component — stock macOS's /tmp -> /private/tmp
// and /var -> /private/var, which is exactly what Go's own
// os.Getwd()/t.TempDir() return unresolved — a second AddOrAttach
// call for an already-created worktree (claude --worktree --resume
// re-firing WorktreeCreate) fell through to `git worktree add`
// against the existing dir and failed with exit 128 instead of the
// documented (false, base, nil) no-op.
//
// A symlink is created explicitly here (rather than relying on the
// test host's own /tmp shape) so the regression reproduces
// deterministically on any OS, not just machines that happen to
// symlink their temp dir.
func TestRunner_AddOrAttach_ResumeIsNoOpThroughSymlink(t *testing.T) {
	src := initBareRepo(t)
	realRoot := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "via-symlink")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink not supported on this host: %v", err)
	}
	dst := filepath.Join(linkRoot, "wt")
	r := NewRunner()

	created, _, err := r.AddOrAttach(context.Background(), src, dst, "F-Resume", "main")
	if err != nil {
		t.Fatalf("first AddOrAttach: %v", err)
	}
	if !created {
		t.Fatalf("first call: created = false, want true")
	}

	created, _, err = r.AddOrAttach(context.Background(), src, dst, "F-Resume", "main")
	if err != nil {
		t.Fatalf("second AddOrAttach through a symlinked dst (simulating macOS /tmp -> /private/tmp): want a silent no-op, got error: %v", err)
	}
	if created {
		t.Errorf("second call: created = true, want false (resume no-op)")
	}
}

// TestRunner_AddOrAttach_RecreatesPrunableWorktree is the regression guard for
// the half-materialised-worktree bug: when a worktree dir is deleted
// out-of-band (a partial teardown, a manual `rm -rf`, an interrupted run) while
// its git admin record survives, `git worktree list` still reports the path —
// as "prunable". The idempotency check matched that path and returned a no-op,
// so `bough create` reported the repo "already registered" and never recreated
// its dir, leaving `claude --worktree` sessions with only the repos whose dirs
// happened to survive. AddOrAttach must instead prune the stale entry and
// re-add, bringing the worktree back on the existing branch.
func TestRunner_AddOrAttach_RecreatesPrunableWorktree(t *testing.T) {
	src := initBareRepo(t)
	// Resolve the temp root's symlinks up front so the path registered with git
	// and the literal dst share a resolved prefix. On hosts where t.TempDir()
	// sits under a symlink (macOS: /var -> /private/var), after os.RemoveAll(dst)
	// EvalSymlinks(dst) fails, so AddOrAttach's resolvedDst falls back to the
	// literal path and never matches git's symlink-resolved `worktree list`
	// entry — the loop would then take the "no match" fall-through and never
	// exercise the os.Stat recovery guard this test exists to protect (the guard
	// could be reverted and the test would still pass). Resolving here forces the
	// intended "match found -> os.Stat fails -> break" path on every OS, the same
	// discipline TestRunner_AddOrAttach_ResumeIsNoOpThroughSymlink uses.
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(tmp, "wt")
	r := NewRunner()

	// First materialisation creates the worktree + branch F-Recover.
	if _, _, err := r.AddOrAttach(context.Background(), src, dst, "F-Recover", "main"); err != nil {
		t.Fatalf("initial add: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err != nil {
		t.Fatalf("worktree dir not created: %v", err)
	}

	// Simulate the out-of-band deletion: remove the dir but leave git's admin
	// record, so `git worktree list` now reports it as prunable.
	if err := os.RemoveAll(dst); err != nil {
		t.Fatal(err)
	}

	// Re-run: must recreate the dir instead of no-op'ing on the stale entry.
	created, _, err := r.AddOrAttach(context.Background(), src, dst, "F-Recover", "main")
	if err != nil {
		t.Fatalf("recovery add: want the prunable worktree re-materialised, got error: %v", err)
	}
	if created {
		t.Errorf("recovery took the -b (new-branch) path; want the attach path (created=false) onto the surviving F-Recover branch")
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err != nil {
		t.Errorf("prunable worktree was not recreated on disk: %v", err)
	}
	wts, err := r.List(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	var onBranch bool
	for _, wt := range wts {
		if wt.Branch == "F-Recover" {
			onBranch = true
		}
	}
	if !onBranch {
		t.Errorf("recreated worktree is not checked out to F-Recover: %+v", wts)
	}
}

func TestRunner_RemoveAndDeleteBranch(t *testing.T) {
	src := initBareRepo(t)
	r := NewRunner()
	dst := filepath.Join(t.TempDir(), "wt")
	if _, _, err := r.AddOrAttach(context.Background(), src, dst, "F-Rm", "main"); err != nil {
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

// TestRunner_DeleteBranch_UsedByWorktreeIsNotSwallowed is the
// regression guard for the wave-2 review finding: DeleteBranch used
// to treat every git exec.ExitError as the idempotent "branch not
// found" case, since `git branch -D` also exits 1 when the branch is
// checked out in another worktree — a real failure that must bubble
// up, not be swallowed as false success.
func TestRunner_DeleteBranch_UsedByWorktreeIsNotSwallowed(t *testing.T) {
	src := initBareRepo(t)
	r := NewRunner()
	dst := filepath.Join(t.TempDir(), "wt-inuse")
	if _, _, err := r.AddOrAttach(context.Background(), src, dst, "F-InUse", "main"); err != nil {
		t.Fatalf("setup AddOrAttach: %v", err)
	}
	// Do NOT Remove the worktree first — the branch is still checked
	// out there, so `git branch -D` must fail with "used by worktree",
	// not "not found".
	err := r.DeleteBranch(context.Background(), src, "F-InUse")
	if err == nil {
		t.Fatal("DeleteBranch on a branch checked out in another worktree: want error, got nil")
	}
	if strings.Contains(err.Error(), "not found") {
		t.Errorf("DeleteBranch misclassified a real failure as 'not found': %v", err)
	}
}

func TestRunner_Remove_fallbackOnPartial(t *testing.T) {
	// Mimic the "interrupted previous teardown" scenario: the worktree
	// directory was nuked out-of-band but git still has a record. Remove
	// should fall through to `git worktree prune` and not error out.
	src := initBareRepo(t)
	r := NewRunner()
	dst := filepath.Join(t.TempDir(), "wt-partial")
	if _, _, err := r.AddOrAttach(context.Background(), src, dst, "F-P", "main"); err != nil {
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
	if _, _, err := r.AddOrAttach(context.Background(), src, dst, "F-List", "main"); err != nil {
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

// TestRunner_DetectBase_slashedBranchName is the regression for the
// create exit-128: origin/HEAD pointing at a slashed branch
// (refs/remotes/origin/feature/x) used to be mangled to "x" by the
// last-slash split, an invalid ref. DetectBase must return the full
// "feature/x".
func TestRunner_DetectBase_slashedBranchName(t *testing.T) {
	src := initBareRepo(t)
	rev, err := exec.Command("git", "-C", src, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(src, ".git", "refs", "remotes", "origin", "feature"), 0o755); err != nil {
		t.Fatalf("mkdir origin/feature: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, ".git", "refs", "remotes", "origin", "feature", "x"), rev, 0o644); err != nil {
		t.Fatalf("write origin/feature/x: %v", err)
	}
	if out, err := exec.Command("git", "-C", src, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/feature/x").CombinedOutput(); err != nil {
		t.Fatalf("symbolic-ref: %v\n%s", err, out)
	}

	base, err := NewRunner().DetectBase(context.Background(), src, "")
	if err != nil {
		t.Fatalf("DetectBase: %v", err)
	}
	if base != "feature/x" {
		t.Errorf("got %q want %q (a slashed default branch must keep its prefix)", base, "feature/x")
	}
}

// TestRunner_AddOrAttach_slashedBase proves a worktree can be branched
// off a base whose name contains a slash (feature/x). Before the
// DetectBase fix the create flow passed the mangled "x" here and git
// died with `fatal: invalid reference` (exit 128).
func TestRunner_AddOrAttach_slashedBase(t *testing.T) {
	src := initBareRepo(t)
	if out, err := exec.Command("git", "-C", src, "branch", "feature/x").CombinedOutput(); err != nil {
		t.Fatalf("create feature/x: %v\n%s", err, out)
	}
	dst := filepath.Join(t.TempDir(), "wt")
	created, _, err := NewRunner().AddOrAttach(context.Background(), src, dst, "F-Slash", "feature/x")
	if err != nil {
		t.Fatalf("AddOrAttach off slashed base: %v", err)
	}
	if !created {
		t.Errorf("expected created=true off slashed base")
	}
}
