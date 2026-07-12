package api

import "path/filepath"

// ResolveUnderRawWorktreeRoot resolves a possibly-relative path against
// the RAW worktree root — the directory that holds every declared
// repository as a sibling. req.WorktreeRoot is the engine-provider
// repo's OWN worktree path (one level deeper), so a path like
// "demo-api/compose.yml" or "demo-api/es-config/analyzer" that names a
// sibling repo resolves against filepath.Dir(worktreeRoot). Absolute
// paths are returned unchanged.
//
// Shared by every engine plugin that references a sibling-repo file
// (compose's compose.file, elasticsearch's es.config_mount) so the
// convention lives in exactly one place.
func ResolveUnderRawWorktreeRoot(worktreeRoot, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(filepath.Dir(worktreeRoot), p)
}
