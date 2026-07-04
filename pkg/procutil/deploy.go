// Package procutil holds the host-process (nix services-flake /
// process-compose) lifecycle helpers that were duplicated verbatim
// across the bough engine plugins' <engine>.go files — the non-docker
// sibling of pkg/dockerutil.
//
// DeployFlake materialises a plugin's embedded nix flake into the
// worktree; LsofListener finds the PID holding a TCP listener;
// KillStrayProcessCompose reaps the process-compose supervisor that
// would otherwise respawn a killed engine.
package procutil

import (
	"io/fs"
	"os"
	"path/filepath"
)

// DeployFlake materialises the embedded asset subtree rooted at subdir
// (e.g. "nix") into dst. Re-running is idempotent: existing files are
// overwritten so a future plugin upgrade picks up the new wrapper
// without manual cleanup. Plugins pass their package-level //go:embed FS
// as assets — embed.FS satisfies fs.FS.
func DeployFlake(assets fs.FS, subdir, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return fs.WalkDir(assets, subdir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(subdir, p)
		if rel == "" || rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(assets, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
