package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
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
	// maxLockNameBytes keeps <name>.lock under the common 255-byte limit.
	maxLockNameBytes = 200
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

// worktreePathFor is <worktrees-dir>/<name>, refusing a name that is not
// one plain directory name there: remove rm -rf's this path, so `..` would
// delete the monorepo root, "feat" would delete a nested "feat/x", and a
// spelling such as "b/" would reach worktrees/b under a name the registry
// and the worktree lock do not know.
func worktreePathFor(monorepoRoot, name string) (string, error) {
	dir := worktreesDir(monorepoRoot)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("worktree name %q must be one directory name inside %s", name, dir)
	}
	if err := checkWorktreeEntry(dir, name); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// checkWorktreeEntry refuses <dir>/<name> when it exists as a symlink or
// only as a case alias of another entry. remove runs pre_remove, Cleanup
// and the git fallback rm -rf inside the worktree, so a symlink sent them
// to its target; a case alias on a case-insensitive filesystem opened
// another worktree whose registry entry and lock key on its real name.
func checkWorktreeEntry(dir, name string) error {
	info, err := os.Lstat(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("worktree %s is a symlink; bough only manages the directories it created", filepath.Join(dir, name))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == name {
			return nil
		}
	}
	return fmt.Errorf("worktree name %q matches an existing directory in %s only by letter case; use its exact name", name, dir)
}

// lockWorktree serializes create and remove of one worktree name across
// processes, from the first step to the last: the registry lock covers
// only its own load-to-save, so a remove could otherwise delete the
// datadir and worktree a concurrent create of the same name was starting
// an engine on. The lock file lives at <root>/.bough/locks/<name>.lock
// and is never deleted (see fsutil.Lock).
func lockWorktree(monorepoRoot, name string) (release func(), err error) {
	file := url.PathEscape(name)
	if len(file) > maxLockNameBytes {
		// Escaping turns each UTF-8 byte into %XX; a long non-ASCII name
		// would exceed the 255-byte file-name limit.
		sum := sha256.Sum256([]byte(name))
		file = hex.EncodeToString(sum[:])
	}
	path := filepath.Join(monorepoRoot, boughDir, locksSubdir, file+".lock")
	release, err = fsutil.Lock(path)
	if err != nil {
		return nil, fmt.Errorf("lock worktree %s: %w", name, err)
	}
	return release, nil
}
