package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/gitwt"
)

// worktreeCreateHook drives the real WorktreeCreate hook contract — the
// same entry point Claude Code invokes — and returns the path the hook
// printed on stdout. That path, and nothing else, is what the host cd's
// into, so it is the thing worth asserting against.
func worktreeCreateHook(t *testing.T, root, name string) string {
	t.Helper()
	cmd := newHookHandleCmd()
	cmd.SetArgs([]string{"--event", "WorktreeCreate", "--out", filepath.Join(root, "obs.jsonl")})
	cmd.SetIn(strings.NewReader(fmt.Sprintf(`{"name":%q,"cwd":%q}`, name, root)))
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("hook handle WorktreeCreate: %v\nstderr:\n%s", err, errBuf.String())
	}
	return strings.TrimSpace(out.String())
}

// TestWorktreeRootIsAnIsolatedWorkTree is the regression for a failure
// that reached an operator's terminal: `claude --worktree <name>` aborted
// with "Refusing to use <container> as an isolation worktree: git resolves
// its working tree to <monorepo root>". Everything bough provisions had
// succeeded; only the host's final cd was rejected, because the container
// was a plain directory inside a git monorepo and git discovery resolved
// it upwards.
//
// The assertion is the host's own predicate, not a proxy for it: the
// emitted path must resolve to ITSELF and must not be running on the
// monorepo's own git directory. Anything weaker (dir exists, sub-repos
// present) was already true when the failure happened.
func TestWorktreeRootIsAnIsolatedWorkTree(t *testing.T) {
	root := t.TempDir()
	gitInitMain(t, root) // the monorepo root is itself a repo — auba's shape
	gitInitMain(t, filepath.Join(root, "demo"))
	writeMinimalBoughYAML(t, root)

	got := worktreeCreateHook(t, root, "F-Iso")

	if !gitwt.NewRunner().SelfResolvingWorkTree(context.Background(), got) {
		top, _ := exec.Command("git", "-C", got, "rev-parse", "--show-toplevel").Output()
		t.Fatalf("emitted worktree root %q is not a self-resolving work tree (git resolves it to %q) — a Claude Code host refuses it",
			got, strings.TrimSpace(string(top)))
	}
	if _, err := os.Stat(filepath.Join(got, "demo", ".git")); err != nil {
		t.Errorf("sub-repo worktree not materialised inside the container: %v", err)
	}
}

// TestContainerStaysEmptyForSubRepos is the regression for what making the
// container a work tree nearly cost: the sub-repo worktrees are added INTO
// it, and `git worktree add` refuses a path that already exists. Checking
// out the root's tree would materialise every tracked top-level entry, so a
// repository sharing a name with one of them could never be materialised —
// measured before the fix as `fatal: '<container>/demo' already exists`,
// reported only as a WARNING while the operator's `demo/` silently held the
// monorepo root's own files.
func TestContainerStaysEmptyForSubRepos(t *testing.T) {
	root := t.TempDir()
	gitInitMain(t, root)
	// The root TRACKS a directory named exactly like the configured repo.
	if err := os.MkdirAll(filepath.Join(root, "demo"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "demo", "root-owned.txt"), []byte("root\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "root tracks demo/"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitInitMain(t, filepath.Join(root, "demo"))
	writeMinimalBoughYAML(t, root)

	got := worktreeCreateHook(t, root, "F-Collide")

	if _, err := os.Stat(filepath.Join(got, "demo", ".git")); err != nil {
		t.Fatalf("sub-repo worktree lost to a name collision with the root's tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(got, "demo", "root-owned.txt")); !os.IsNotExist(err) {
		t.Errorf("the container holds the monorepo root's own file; it must start empty (err=%v)", err)
	}
	// And the container must still be clean: --no-checkout without the empty
	// sparse-checkout leaves the index claiming every tracked file exists.
	out, err := exec.Command("git", "-C", got, "status", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(line, "D ") || strings.HasPrefix(line, " D") {
			t.Errorf("container reports tracked files as deleted — one careless commit from a mass deletion:\n%s", out)
			break
		}
	}
}

// TestWorktreeRootOutsideAnyRepoStaysAPlainDir is the other direction, and
// it matters as much: when the monorepo root is NOT a repo there is
// nothing above the container to capture git's resolution, so a plain
// directory is correct and bough must not start requiring a repo it was
// documented not to need (the non-git escape hatch, warnIfRootNotGit).
// Without this, "make the container a work tree" could quietly become
// "bough only works in a git monorepo".
func TestWorktreeRootOutsideAnyRepoStaysAPlainDir(t *testing.T) {
	root := t.TempDir() // deliberately NOT a git repo
	gitInitMain(t, filepath.Join(root, "demo"))
	writeMinimalBoughYAML(t, root)

	got := worktreeCreateHook(t, root, "F-Plain")

	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Fatalf("no worktree root at %q: err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(got, "demo", ".git")); err != nil {
		t.Errorf("sub-repo worktree not materialised: %v", err)
	}
}

// TestRemoveLeavesNoWorktreeRecord pins the teardown half. The container
// is now registered in the monorepo root's worktree list, so tearing it
// down with rm -rf alone would leave an admin entry naming a directory
// that no longer exists — visible in `git worktree list` and, worse, in
// the way of re-creating the same name later.
func TestRemoveLeavesNoWorktreeRecord(t *testing.T) {
	root := t.TempDir()
	gitInitMain(t, root)
	gitInitMain(t, filepath.Join(root, "demo"))
	writeMinimalBoughYAML(t, root)

	wt := worktreeCreateHook(t, root, "F-Gone")

	cmd := newHookHandleCmd()
	cmd.SetArgs([]string{"--event", "WorktreeRemove", "--out", filepath.Join(root, "obs.jsonl")})
	cmd.SetIn(strings.NewReader(fmt.Sprintf(`{"worktree_path":%q}`, wt)))
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("hook handle WorktreeRemove: %v\nstderr:\n%s", err, errBuf.String())
	}

	listed, err := exec.Command("git", "-C", root, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	if strings.Contains(string(listed), wt) {
		t.Errorf("monorepo root still records the removed container:\n%s", listed)
	}
}
