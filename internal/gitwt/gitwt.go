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
	"path/filepath"
	"strings"

	"github.com/ikeikeikeike/bough/internal/fsutil"
)

// Worktree is one entry of `git worktree list --porcelain`.
type Worktree struct {
	Path     string // absolute path
	HEAD     string // commit hash
	Branch   string // branch shortname ("" for detached HEAD)
	Prunable bool   // true when git itself flags this entry as removable by `git worktree prune`
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
// the `.bough.yaml` branch_strategy field.
func (r *Runner) DetectBase(ctx context.Context, repoPath, fallback string) (string, error) {
	out, err := r.cmd(ctx, "git", "-C", repoPath, "symbolic-ref", "refs/remotes/origin/HEAD").Output()
	if err != nil {
		if fallback != "" {
			return fallback, nil
		}
		return "", fmt.Errorf("gitwt: detect base for %s: %w", repoPath, err)
	}
	s := strings.TrimSpace(string(out))
	// s is `refs/remotes/<remote>/<branch>`, and <branch> may itself
	// contain slashes (origin/HEAD → refs/remotes/origin/feature/x).
	// Strip the `refs/remotes/` prefix, then the remote-name segment,
	// keeping the full branch path. The previous code split on the
	// *last* slash, which silently dropped the leading path of any
	// slashed branch name — `feature/x` became `x`, an invalid ref that
	// made the subsequent `git worktree add … <base>` fail with
	// `fatal: invalid reference` (exit 128).
	s = strings.TrimPrefix(s, "refs/remotes/")
	if idx := strings.Index(s, "/"); idx >= 0 {
		return s[idx+1:], nil
	}
	return s, nil
}

// AddOrAttach creates a new worktree at `dst`. When `branch` does not
// yet exist locally, it is created off `base` via `-b`. When it does
// exist (e.g. the user previously ran `bough remove F-X` but kept the
// branch, or another orchestrator pushed it), the worktree is attached
// to the existing branch. Returns `created=true` for the first path
// and false for the attach path, plus `effectiveBase` — the ref the
// worktree was actually branched from (origin/<base> after a live
// fetch, else the local <base>) — so the caller logs the true source.
//
// Idempotency: if `dst` is already a registered worktree AND its dir
// still exists on disk AND git does not itself flag the entry
// "prunable", the call returns `(false, base, nil)` immediately. This
// makes the hook safe to re-invoke — Claude Code re-fires
// WorktreeCreate every time the user runs
// `claude --worktree F-X --resume <session>`, so a "second call is a
// no-op" contract is required.
//
// Recovery: two stale states are healed instead of no-op'd. (a) `dst`
// is registered but git considers it "prunable" — either the whole dir
// is gone (a partial teardown or an out-of-band `rm -rf`) or the dir
// survives but its own `.git` link (or git's admin-side record) was
// removed independently — so a bare path match is not proof of
// materialisation; and (b) the dir exists but is NOT in
// `git worktree list` (a leftover orphan from an interrupted prior run).
// In both cases Prune runs first so the subsequent `worktree add` is not
// blocked by leftover git admin state, and the worktree is
// re-materialised (attach path, on the existing branch).
func (r *Runner) AddOrAttach(ctx context.Context, repoPath, dst, branch, base string) (created bool, effectiveBase string, err error) {
	// effectiveBase is the ref the worktree was actually branched from,
	// returned so the caller logs what seeded it (origin/<base> vs the
	// local <base>) instead of inferring from r.Fetch — a fetch can be
	// enabled yet fail, in which case the branch is cut from the local base.
	effectiveBase = base
	if wts, listErr := r.List(ctx, repoPath); listErr == nil {
		// git worktree list --porcelain reports the symlink-resolved
		// real path, but dst is the caller's literal path — on stock
		// macOS (/tmp -> /private/tmp, /var -> /private/var) those
		// differ even though they name the same directory, since
		// os.Getwd()/$PWD (what callers build dst from) do not resolve
		// symlinks. Resolve dst the same way before comparing so an
		// already-registered worktree is actually recognized; a
		// resolve failure (dst doesn't exist yet) falls back to the
		// literal path, which is also what a genuinely-new dst needs.
		resolvedDst := dst
		if real, evalErr := filepath.EvalSymlinks(dst); evalErr == nil {
			resolvedDst = real
		}
		for _, wt := range wts {
			if wt.Path == resolvedDst {
				// A path match is only a materialised no-op when the
				// worktree dir still exists on disk AND git does not
				// itself consider the entry prunable. `git worktree list`
				// flags "prunable" whenever `git worktree prune` would
				// remove the entry — not only when the whole dir is gone
				// (os.Stat ENOENT), but also when the dir survives while
				// its own `.git` file (or the admin-side gitdir record) was
				// removed independently, e.g. a partial teardown that
				// cleared `.git` before the rest of the tree, or a cleanup
				// script that strips dotfiles. A bare os.Stat cannot see
				// that second case — the dir is right there — so trust
				// git's own verdict first and treat ENOENT as a fallback
				// for git versions/entries where the annotation is absent.
				// Either signal firing is what left `claude --worktree`
				// sessions with a half-materialised worktree: the entry was
				// reported "already registered" and never recreated. Fall
				// through to the prune + add below so it comes back (attach
				// path, on the existing branch).
				//
				// A stat that fails for a reason other than ENOENT (EACCES
				// on a parent dir, a momentarily-stale NFS mount) is not
				// proof of prunability by itself and keeps the old no-op —
				// unless git's own prunable flag already fired — rather
				// than tear down a worktree that might actually be present.
				_, statErr := os.Stat(dst)
				if !wt.Prunable && !errors.Is(statErr, os.ErrNotExist) {
					return false, base, nil
				}
				break
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
	if r.Fetch {
		if fetchErr := r.cmd(ctx, "git", "-C", repoPath, "fetch", "--quiet", "origin", base).Run(); fetchErr == nil {
			effectiveBase = "origin/" + base
		}
	}

	// --no-track: when effectiveBase is the remote-tracking origin/<base>,
	// `git worktree add -b` would set the new branch's upstream to
	// origin/<base>. auba publishes feature branches with a bare `git push`
	// (push.default=simple), which then refuses because the upstream is
	// origin/<base>, not origin/<branch>; --no-track leaves the branch
	// upstream-less so the first `git push -u` wins. Harmless no-op when
	// effectiveBase is a local branch.
	//
	// Capture combined output so a git failure surfaces its actual
	// message (e.g. `fatal: invalid reference: <base>`) instead of a
	// bare "exit status 128" — create swallowing this is what made the
	// DetectBase slash bug above so hard to diagnose in the field.
	newOut, newErr := r.cmd(ctx, "git", "-C", repoPath, "worktree", "add", "--no-track", dst, "-b", branch, effectiveBase).CombinedOutput()
	if newErr == nil {
		return true, effectiveBase, nil
	}
	attachOut, attachErr := r.cmd(ctx, "git", "-C", repoPath, "worktree", "add", dst, branch).CombinedOutput()
	if attachErr == nil {
		return false, base, nil
	}
	return false, base, fmt.Errorf("gitwt: add %s @ %s failed (new: %v: %s; attach: %v: %s)",
		branch, dst, newErr, strings.TrimSpace(string(newOut)),
		attachErr, strings.TrimSpace(string(attachOut)))
}

// SelfResolvingWorkTree reports whether `dir` is a git work tree that
// resolves to ITSELF — the predicate a Claude Code host applies to any
// directory offered as an isolation worktree.
//
// It exists because a directory can sit inside a repository without
// being a work tree of its own: git's discovery then walks UP and
// resolves the tree to an ancestor checkout, so a command run in `dir`
// writes outside `dir`. A host that hands sessions an isolated tree
// cannot accept that, and refuses.
//
// Resolution is the WHOLE test, deliberately. An earlier version also
// demanded that the git dir differ from `<dir>/.git`, meaning to pin
// "linked worktree, not the shared checkout". That extra clause rejects a
// container which is simply its own standalone repository — and a host
// accepts those (measured: a hook returning such a container starts the
// session normally). A predicate stricter than the thing it models does
// not fail safe; it tells the operator to rebuild something that works.
//
// A path that is not in a repository at all returns false — "not a work
// tree" is not "resolves to itself", and callers that want to allow the
// non-git case test for it separately.
//
// It is a method rather than a free function so it goes through the same
// injectable cmd seam and the same context as every other git call here:
// AddDetached's own guard is this check, and a Runner built with a fake
// cmd must be able to make that guard observe the fake.
func (r *Runner) SelfResolvingWorkTree(ctx context.Context, dir string) bool {
	top, err := r.cmd(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return false
	}
	// Compare resolved paths: on macOS /tmp is a symlink to /private/tmp,
	// and git reports the resolved spelling while the caller holds the
	// literal one — the same directory under two names.
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
	if err != nil {
		return false
	}
	return got == want
}

// ErrPopulatedContainer says the target directory already has contents,
// so `git worktree add` cannot adopt it. It is a distinct error because
// it is the ORDINARY state of a worktree created before containers became
// work trees — the caller degrades quietly rather than reporting a
// failure the operator cannot act on and would then see on every hook
// fire for the rest of that worktree's life.
var ErrPopulatedContainer = errors.New("gitwt: container already exists and is not empty")

// AddDetached materialises `dst` as a DETACHED worktree of the repo at
// repoPath — a work tree with no branch of its own.
//
// The monorepo container `bough create` hands back is a plain directory
// holding one sub-repo worktree per repository. Inside a git monorepo
// that container is not a work tree, so git resolves it to the monorepo
// root and a Claude Code host refuses it (see SelfResolvingWorkTree).
// Making the container itself a work tree removes the condition instead
// of documenting around it.
//
// Detached rather than branched on purpose: the container carries no
// commits of its own, and cutting a branch per worktree would collide
// with the teardown policy that deliberately keeps feature branches —
// every create would leave a branch nothing ever deletes.
//
// Idempotent, and deliberately conservative about what it will touch: an
// existing self-resolving work tree is a no-op, and a directory that
// already has contents is left exactly as it is. `git worktree add`
// cannot adopt a populated directory anyway, so containers created
// before this existed keep working as plain directories rather than
// being torn down underneath a running session.
func (r *Runner) AddDetached(ctx context.Context, repoPath, dst string) error {
	if r.SelfResolvingWorkTree(ctx, dst) {
		return nil
	}
	if entries, err := os.ReadDir(dst); err == nil && len(entries) > 0 {
		return fmt.Errorf("%w: %s", ErrPopulatedContainer, dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("gitwt: mkdir parent of %s: %w", dst, err)
	}
	// An empty dir left by a previous run blocks `worktree add`; remove it
	// (empty, so nothing can be lost) and let git create the dir itself.
	_ = os.Remove(dst)
	// Best-effort prune first: a stale admin entry pointing at an
	// already-deleted dst would otherwise block the add.
	_ = r.cmd(ctx, "git", "-C", repoPath, "worktree", "prune").Run()
	// The container is checked out at a commit whose tree is EMPTY, not at
	// HEAD. It exists to hold one sub-repo worktree per repository, and
	// `git worktree add` refuses a path that already exists — so checking
	// out the root's tree here would materialise every tracked top-level
	// entry, and a repository sharing a name with one of them could never
	// be given its own worktree. Measured before this: a root tracking
	// `alpha/` plus a repo named `alpha` failed with
	// "'<container>/alpha' already exists", and the operator's `alpha/`
	// silently held the root's files instead.
	//
	// An empty tree also keeps `git status` honest. --no-checkout with a
	// sparse-checkout hides the files but leaves index entries behind, and
	// once a sub-repo worktree materialises a directory over one of those
	// paths git reports it as a staged deletion — a mass deletion one
	// careless `commit -a` away. With no index entries there is nothing to
	// report but the untracked sub-repo dirs.
	base, err := r.emptyCommit(ctx, repoPath)
	if err != nil {
		return err
	}
	out, err := r.cmd(ctx, "git", "-C", repoPath, "worktree", "add", "--detach", dst, base).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gitwt: worktree add --detach %s: %w (%s)", dst, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// emptyCommit returns a commit in repoPath whose tree has no entries.
//
// Identity and dates are pinned so the commit hash is the same every time:
// the object is written once and reused by every later container rather
// than leaving one dangling commit per `bough create`. The pinned identity
// also means this works in an environment with no user.name/user.email
// configured, which `git commit-tree` would otherwise refuse.
func (r *Runner) emptyCommit(ctx context.Context, repoPath string) (string, error) {
	tree, err := r.cmd(ctx, "git", "-C", repoPath, "hash-object", "-w", "-t", "tree", os.DevNull).Output()
	if err != nil {
		return "", fmt.Errorf("gitwt: write empty tree: %w", err)
	}
	cmd := r.cmd(ctx, "git", "-C", repoPath, "commit-tree", strings.TrimSpace(string(tree)), "-m", emptyContainerSubject)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=bough", "GIT_AUTHOR_EMAIL=bough@localhost", "GIT_AUTHOR_DATE=@0 +0000",
		"GIT_COMMITTER_NAME=bough", "GIT_COMMITTER_EMAIL=bough@localhost", "GIT_COMMITTER_DATE=@0 +0000",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gitwt: commit-tree for the empty container: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// emptyContainerSubject is part of the pinned commit's hash, so changing it
// changes which object every container is checked out at. It is only ever
// read by a human running `git log` on a container.
const emptyContainerSubject = "bough: empty worktree container"

// Clone acquires a repo into dst when it is not already present. A
// remote git URL (git@host:org/repo, https://…, ssh://…, file://…) is
// cloned over its transport; any other value is treated as a local
// filesystem path and cloned with `--local` (hardlink-fast, offline). A
// leading `~` is expanded and a relative local path is resolved against
// baseDir (the monorepo root) so `source: ../auba-proto` is unambiguous.
// CombinedOutput is captured so a clone failure surfaces git's message.
func (r *Runner) Clone(ctx context.Context, source, dst, baseDir string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("gitwt.Clone: mkdir parent of %s: %w", dst, err)
	}
	args := []string{"clone"}
	src := source
	if !isRemoteURL(source) {
		src = fsutil.ExpandHome(source)
		if !filepath.IsAbs(src) {
			src = filepath.Join(baseDir, src)
		}
		args = append(args, "--local")
	}
	args = append(args, src, dst)
	if out, err := r.cmd(ctx, "git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("gitwt.Clone: git clone %s → %s: %w: %s",
			source, dst, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// isRemoteURL reports whether source is a remote git URL rather than a
// local filesystem path. Remote = contains "://" (https / ssh / file)
// or is scp-like `[user@]host:path` — a ':' that appears before any '/'.
// git's scp syntax does NOT require a user, so a userless `host:org/repo`
// and an ssh-config alias `gh:org/repo` are remotes too (the previous
// `@`-required heuristic misrouted those to --local). A local path's
// first ':' (if any) only appears after a '/' (e.g. /a/b:c) or not at
// all. Everything else is a local path → cloned with --local.
func isRemoteURL(source string) bool {
	if strings.Contains(source, "://") {
		return true
	}
	colon := strings.Index(source, ":")
	slash := strings.Index(source, "/")
	return colon >= 0 && (slash < 0 || colon < slash)
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
	out, err := r.cmd(ctx, "git", "-C", repoPath, "branch", "-D", branch).CombinedOutput()
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	// git also exits 1 for "cannot delete branch '<b>' used by worktree
	// at '<path>'" — a real failure that must bubble, not be swallowed
	// as idempotent success. Only the actual "not found" message is
	// treated as already-deleted.
	if errors.As(err, &ee) && strings.Contains(string(out), "not found") {
		return nil
	}
	return fmt.Errorf("gitwt: branch -D %s: %w (%s)", branch, err, strings.TrimSpace(string(out)))
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
		case strings.HasPrefix(line, "prunable"):
			// "prunable" or "prunable <reason>" — git's own verdict on
			// whether `git worktree prune` would remove this entry (dir
			// missing, gitdir file broken, etc). See AddOrAttach, which
			// trusts this over re-deriving "is this dir really gone" from
			// a single os.Stat.
			cur.Prunable = true
		}
	}
	flush()
	return wts, nil
}
