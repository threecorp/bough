package gitwt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RepairInPlace converts a populated legacy container at dst into a
// detached work tree of the repo at repoPath WITHOUT touching its
// contents. Containers created before AddDetached existed are plain
// directories: git resolves them up to the monorepo root, an isolating
// host refuses to cd into them — and, worse than refusing, drops the
// session's worktree binding and keeps running unisolated.
//
// `git worktree add` cannot adopt a populated directory, so the admin
// entry is created at a throwaway sibling path against the same pinned
// empty-tree commit AddDetached uses; only its `.git` link is moved into
// the container, and `git worktree repair` re-points the admin record at
// the real location. Measured on live containers: contents, sub-repo
// worktrees and symlinks all survive, and `git status` in the container
// reports only the untracked entries it already had.
//
// Idempotent: a container that already resolves to itself is a no-op.
// Conservative: a container holding a real `.git` directory of its own
// is left alone (any existing .git — file or directory — is refused
// before anything is touched), and the throwaway admin entry is rolled
// back so no stale record survives a failure.
func (r *Runner) RepairInPlace(ctx context.Context, repoPath, dst string) error {
	if r.SelfResolvingWorkTree(ctx, dst) {
		return nil
	}
	if _, err := os.Stat(dst); err != nil {
		return fmt.Errorf("gitwt: repair %s: %w", dst, err)
	}
	// A container that already carries git state of its own is not the
	// legacy shape this converts, and rename(2) would not protect it: it
	// only refuses when the DESTINATION is a directory, so a `.git` FILE —
	// a linked worktree whose admin entry was pruned, or whose source
	// checkout moved — would be silently overwritten and the container
	// re-parented to the monorepo's empty tree, orphaning the branch it had
	// checked out. Refuse either shape and say which.
	if fi, err := os.Lstat(filepath.Join(dst, ".git")); err == nil {
		kind := "file"
		if fi.IsDir() {
			kind = "directory"
		}
		return fmt.Errorf("gitwt: repair %s: refusing to replace the existing .git %s (this container carries git state of its own)", dst, kind)
	}
	base, err := r.emptyCommit(ctx, repoPath)
	if err != nil {
		return err
	}
	tmp := filepath.Join(repoPath, ".bough-repair-tmp", filepath.Base(dst))
	if err := os.MkdirAll(filepath.Dir(tmp), 0o755); err != nil {
		return fmt.Errorf("gitwt: repair %s: mkdir staging dir: %w", dst, err)
	}
	// Empty-only removal: a concurrent repair's staging entry survives.
	defer func() { _ = os.Remove(filepath.Dir(tmp)) }()
	// A staging path left behind by an interrupted run would make every
	// later repair of this container die on "already exists" — with nothing
	// in the error naming the directory to delete. Clear it first: it only
	// ever holds an empty-tree worktree, so nothing of the operator's can
	// be in it.
	if _, err := os.Stat(tmp); err == nil {
		_ = r.cmd(ctx, "git", "-C", repoPath, "worktree", "remove", "--force", tmp).Run()
		_ = os.RemoveAll(tmp)
		_ = r.cmd(ctx, "git", "-C", repoPath, "worktree", "prune").Run()
	}
	out, err := r.cmd(ctx, "git", "-C", repoPath, "worktree", "add", "--detach", tmp, base).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gitwt: repair %s: staging worktree add: %w (%s)", dst, err, strings.TrimSpace(string(out)))
	}
	// Every failure from here on rolls the container back to the plain
	// directory it was. A half-converted container — a .git link whose
	// admin record was never re-pointed — is worse than the legacy shape
	// this is fixing: the operator gets neither isolation nor a directory
	// git can explain.
	rollback := func() {
		_ = os.Rename(filepath.Join(dst, ".git"), filepath.Join(tmp, ".git"))
		_ = r.cmd(ctx, "git", "-C", repoPath, "worktree", "remove", "--force", tmp).Run()
	}
	if err := os.Rename(filepath.Join(tmp, ".git"), filepath.Join(dst, ".git")); err != nil {
		// The move never happened (e.g. dst owns a real .git directory),
		// so only the staging entry needs removing.
		_ = r.cmd(ctx, "git", "-C", repoPath, "worktree", "remove", "--force", tmp).Run()
		return fmt.Errorf("gitwt: repair %s: move .git link: %w", dst, err)
	}
	if out, err := r.cmd(ctx, "git", "-C", repoPath, "worktree", "repair", dst).CombinedOutput(); err != nil {
		rollback()
		return fmt.Errorf("gitwt: repair %s: worktree repair: %w (%s)", dst, err, strings.TrimSpace(string(out)))
	}
	if !r.SelfResolvingWorkTree(ctx, dst) {
		rollback()
		return fmt.Errorf("gitwt: repair %s: container still resolves outside itself", dst)
	}
	// Only now is the staging path disposable: it is empty, and the admin
	// record points at dst.
	_ = os.Remove(tmp)
	return nil
}
