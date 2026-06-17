// Package gitwt is a typed wrapper around the `git worktree` family of
// commands. It centralises the create / attach / remove / prune
// sequences the bough host CLI needs and keeps the exec.Command call
// sites out of the higher layers.
//
// The semantics follow the established `git worktree` idiom used by
// prior in-house Bash hooks — try a fresh `-b <branch> <base>` first,
// fall back to attaching an existing branch on failure, and on remove
// degrade to a `rm -rf + git worktree prune` cleanup when
// `git worktree remove --force` itself fails (which happens when the
// worktree dir was already partially removed out-of-band).
package gitwt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Worktree is one entry of `git worktree list --porcelain`.
type Worktree struct {
	Path   string // absolute path
	HEAD   string // commit hash
	Branch string // branch shortname ("" for detached HEAD)
}

// Runner exposes the wrapped git operations. The cmd field is injectable
// so tests can swap in a fake; production callers use NewRunner().
//
// Fetch controls whether AddOrAttach runs `git fetch origin <base>`
// before `git worktree add`, and whether it uses `origin/<base>`
// instead of the local `<base>` ref as the new branch's starting
// point. Defaults to false (= existing behaviour, used by tests
// against bare local repos that have no origin). bough's `create`
// command sets it to true so a stale local checkout never silently
// branches the worktree off an N-commit-old base.
type Runner struct {
	cmd   func(ctx context.Context, name string, args ...string) *exec.Cmd
	Fetch bool
}

// NewRunner returns a Runner that shells out to the system `git` via
// os/exec. The Nix devShell provides git on the PATH, and bough's
// distribution requires git ≥ 2.30 (where `worktree list --porcelain`
// is stable and `branch -D` works on already-deleted worktrees).
func NewRunner() *Runner {
	return &Runner{cmd: exec.CommandContext}
}

// HeadSHA returns the abbreviated commit hash currently checked out in
// `worktreePath`. Used by the host's stderr observability so the
// operator can see exactly which base SHA each sub-repo worktree was
// materialised from.
func (r *Runner) HeadSHA(ctx context.Context, worktreePath string) (string, error) {
	out, err := r.cmd(ctx, "git", "-C", worktreePath, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// DetectBase resolves the default branch from origin/HEAD
// (refs/remotes/origin/HEAD → develop or master, depending on the
// upstream's configured HEAD). When the symbolic ref is absent (a fresh
// clone with no `git remote set-head` ever run), `fallback` is returned
// without raising — the caller can default to "develop" or "master" via
// the .worktree-isolation.yaml branch_strategy field.
func (r *Runner) DetectBase(ctx context.Context, repoPath, fallback string) (string, error) {
	out, err := r.cmd(ctx, "git", "-C", repoPath, "symbolic-ref", "refs/remotes/origin/HEAD").Output()
	if err != nil {
		if fallback != "" {
			return fallback, nil
		}
		return "", fmt.Errorf("gitwt: detect base for %s: %w", repoPath, err)
	}
	s := strings.TrimSpace(string(out))
	// `refs/remotes/origin/develop` → `develop`. Splitting on the last
	// slash also handles the edge case where the remote name itself
	// contains a slash (rare but legal in git).
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		return s[idx+1:], nil
	}
	return s, nil
}

// AddOrAttach creates a new worktree at `dst`. When `branch` does not
// yet exist locally, it is created off `base` via `-b`. When it does
// exist (e.g. the user previously ran `bough remove F-X` but kept the
// branch, or another orchestrator pushed it), the worktree is attached
// to the existing branch. Returns `created=true` for the first path
// and false for the attach path so the caller can log accordingly.
//
// Idempotency: if `dst` is already a registered worktree, the call
// returns `(false, nil)` immediately. This makes the hook safe to
// re-invoke — Claude Code re-fires WorktreeCreate every time the user
// runs `claude --worktree F-X --resume <session>`, so a "second call
// is a no-op" contract is required (cf. threecorp's
// scripts/worktree-create.sh:46-50 "skip if worktree already exists").
//
// If the dir exists but is NOT in `git worktree list` (= a stale
// orphan from an interrupted prior run), Prune is invoked first so
// the subsequent `worktree add` is not blocked by leftover git admin
// state.
func (r *Runner) AddOrAttach(ctx context.Context, repoPath, dst, branch, base string) (created bool, err error) {
	if wts, listErr := r.List(ctx, repoPath); listErr == nil {
		for _, wt := range wts {
			if wt.Path == dst {
				return false, nil
			}
		}
	}
	// Best-effort prune so a stale worktree entry pointing at an
	// already-removed dst does not block the add below.
	_ = r.cmd(ctx, "git", "-C", repoPath, "worktree", "prune").Run()

	// When Fetch is enabled, refresh origin/<base> before deciding
	// the starting point so a local checkout that has not been
	// pulled in days does not silently branch from a stale ref. If
	// fetch fails (no network, no origin remote, base not on origin)
	// we degrade to the local <base> so an operator working offline
	// still gets a worktree.
	effectiveBase := base
	if r.Fetch {
		if fetchErr := r.cmd(ctx, "git", "-C", repoPath, "fetch", "--quiet", "origin", base).Run(); fetchErr == nil {
			effectiveBase = "origin/" + base
		}
	}

	newErr := r.cmd(ctx, "git", "-C", repoPath, "worktree", "add", dst, "-b", branch, effectiveBase).Run()
	if newErr == nil {
		return true, nil
	}
	attachErr := r.cmd(ctx, "git", "-C", repoPath, "worktree", "add", dst, branch).Run()
	if attachErr == nil {
		return false, nil
	}
	return false, fmt.Errorf("gitwt: add %s @ %s failed (new: %v; attach: %v)", branch, dst, newErr, attachErr)
}

// Remove tears down a worktree via `git worktree remove`. When that
// command fails (e.g. because the worktree directory was already
// partially removed by a previous interrupted teardown), Remove
// degrades to `rm -rf <dst>` + `git worktree prune` so a re-running
// hook always converges to "no record of this worktree".
func (r *Runner) Remove(ctx context.Context, repoPath, dst string, force bool) error {
	args := []string{"-C", repoPath, "worktree", "remove", dst}
	if force {
		args = append(args, "--force")
	}
	if err := r.cmd(ctx, "git", args...).Run(); err == nil {
		return nil
	}
	// Fallback: manual rm + prune. Matches the standard recovery
	// sequence prior in-house hooks have used for this failure mode.
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("gitwt: fallback rm of %s: %w", dst, err)
	}
	if err := r.cmd(ctx, "git", "-C", repoPath, "worktree", "prune").Run(); err != nil {
		return fmt.Errorf("gitwt: prune after fallback rm: %w", err)
	}
	return nil
}

// DeleteBranch runs `git branch -D` on `repoPath`. The bough host
// invokes this after Remove to keep `branch list` clean — recreating
// the same feature name later should start from `base` rather than
// continue an abandoned line of development.
//
// A non-existent branch is not an error (DeleteBranch swallows the
// "not found" exit code from git ≥ 2.30 so re-running the teardown is
// idempotent).
func (r *Runner) DeleteBranch(ctx context.Context, repoPath, branch string) error {
	err := r.cmd(ctx, "git", "-C", repoPath, "branch", "-D", branch).Run()
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		// git exits 1 for "no such branch" — treat as idempotent success.
		// Anything else (permission denied, corrupt repo) is bubbled.
		return nil
	}
	return fmt.Errorf("gitwt: branch -D %s: %w", branch, err)
}

// List parses `git worktree list --porcelain` so the caller can drive a
// `bough backfill` (or `bough status`) without re-implementing the
// porcelain parser. Each blank line in the porcelain output separates
// one Worktree from the next.
func (r *Runner) List(ctx context.Context, repoPath string) ([]Worktree, error) {
	out, err := r.cmd(ctx, "git", "-C", repoPath, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("gitwt: worktree list: %w", err)
	}
	var wts []Worktree
	var cur Worktree
	flush := func() {
		if cur.Path != "" {
			wts = append(wts, cur)
			cur = Worktree{}
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "worktree "):
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			cur.HEAD = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		}
	}
	flush()
	return wts, nil
}
