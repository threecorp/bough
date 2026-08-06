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
// is left alone (the rename refuses to clobber it), and the throwaway
// admin entry is rolled back so no stale record survives a failure.
func (r *Runner) RepairInPlace(ctx context.Context, repoPath, dst string) error {
	if r.SelfResolvingWorkTree(ctx, dst) {
		return nil
	}
	if _, err := os.Stat(dst); err != nil {
		return fmt.Errorf("gitwt: repair %s: %w", dst, err)
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
	out, err := r.cmd(ctx, "git", "-C", repoPath, "worktree", "add", "--detach", tmp, base).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gitwt: repair %s: staging worktree add: %w (%s)", dst, err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(filepath.Join(tmp, ".git"), filepath.Join(dst, ".git")); err != nil {
		// A failed move (e.g. dst owns a real .git directory) must not
		// leave the staging entry behind in `git worktree list`.
		_ = r.cmd(ctx, "git", "-C", repoPath, "worktree", "remove", "--force", tmp).Run()
		return fmt.Errorf("gitwt: repair %s: move .git link: %w", dst, err)
	}
	_ = os.Remove(tmp)
	if out, err := r.cmd(ctx, "git", "-C", repoPath, "worktree", "repair", dst).CombinedOutput(); err != nil {
		return fmt.Errorf("gitwt: repair %s: worktree repair: %w (%s)", dst, err, strings.TrimSpace(string(out)))
	}
	if !r.SelfResolvingWorkTree(ctx, dst) {
		return fmt.Errorf("gitwt: repair %s: container still resolves outside itself", dst)
	}
	return nil
}
