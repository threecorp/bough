// Package procutil holds the host-process helpers bough needs outside
// pkg/dockerutil: DeployAssets materialises an embedded asset subtree
// into a directory, and LsofListener finds the PID holding a TCP
// listener.
package procutil

import (
	"io/fs"
	"os"
	"path/filepath"
)

// DeployAssets materialises the embedded asset subtree rooted at subdir
// (e.g. "skills", "commands") into dst. Re-running is idempotent:
// existing files are overwritten so a future upgrade picks up new content
// without manual cleanup. Callers pass their package-level //go:embed FS as
// assets — embed.FS satisfies fs.FS.
func DeployAssets(assets fs.FS, subdir, dst string) error {
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
