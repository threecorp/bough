package cli

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/ikeikeikeike/bough/internal/fsutil"
)

// Workspace layout (v0.11+): everything bough generates at the monorepo
// root is grouped so that a git-initialised monorepo root needs only two
// .gitignore entries — `.bough/` and `worktrees/`.
//
//	<root>/.bough/repos/<name>   source checkouts   (was <root>/<name>)
//	<root>/.bough/ports.json     port registry      (was <root>/.bough-ports.json)
//	<root>/worktrees/<name>      per-feature trees   (was <root>/.worktrees/<name>)
//
// The resolvers below keep every pre-v0.11 layout working unchanged: a
// monorepo whose repos still sit at <root>/<name> and whose worktrees
// still sit under <root>/.worktrees/ is detected and kept in place, so
// upgrading bough never orphans an already-materialised workspace. Only
// freshly-created artifacts adopt the new locations.
const (
	boughDir      = ".bough"
	reposSubdir   = "repos"
	portsFile     = "ports.json"
	worktreesName = "worktrees"
	legacyWtName  = ".worktrees"
	locksSubdir   = "locks"
)

// resolveRepoSrc answers "where does this repo's source checkout live?"
// Preference order:
//  1. the new <root>/.bough/repos/<name> when already acquired there,
//  2. an existing root-level <root>/<name> checkout (pre-v0.11 layout),
//  3. otherwise the new location — a fresh `source:` clone targets it.
//
// "Acquired" is isGitRepo (a `.git` entry), matching the clone guard in
// materializeRepositories: a stray empty dir at either location must not
// masquerade as the repo.
func resolveRepoSrc(monorepoRoot, name string) string {
	newLoc := filepath.Join(monorepoRoot, boughDir, reposSubdir, name)
	if isGitRepo(newLoc) {
		return newLoc
	}
	if oldLoc := filepath.Join(monorepoRoot, name); isGitRepo(oldLoc) {
		return oldLoc
	}
	return newLoc
}

// worktreesDir answers "which directory holds this monorepo's worktrees?"
// A monorepo that already has the legacy hidden <root>/.worktrees/ keeps
// using it so existing worktrees stay findable by remove / verify /
// backfill; a fresh monorepo uses the non-hidden <root>/worktrees/.
func worktreesDir(monorepoRoot string) string {
	if legacy := filepath.Join(monorepoRoot, legacyWtName); isDir(legacy) {
		return legacy
	}
	return filepath.Join(monorepoRoot, worktreesName)
}

// worktreePathFor is <worktrees-dir>/<name>, refusing a name that resolves
// to the worktrees dir itself or outside it: remove rm -rf's this path, so
// `--name ..` would otherwise delete the monorepo root.
func worktreePathFor(monorepoRoot, name string) (string, error) {
	dir := worktreesDir(monorepoRoot)
	path := filepath.Join(dir, name)
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("worktree name %q must name a directory inside %s", name, dir)
	}
	return path, nil
}

// lockWorktree serializes create and remove of one worktree name across
// processes, from the first step to the last: the registry lock covers
// only its own load-to-save, so a remove could otherwise delete the
// datadir and worktree a concurrent create of the same name was starting
// an engine on. The lock file lives at <root>/.bough/locks/<name>.lock
// and is never deleted (see fsutil.Lock).
func lockWorktree(monorepoRoot, name string) (release func(), err error) {
	path := filepath.Join(monorepoRoot, boughDir, locksSubdir, url.PathEscape(name)+".lock")
	release, err = fsutil.Lock(path)
	if err != nil {
		return nil, fmt.Errorf("lock worktree %s: %w", name, err)
	}
	return release, nil
}
