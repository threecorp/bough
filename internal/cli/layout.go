package cli

import "path/filepath"

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
