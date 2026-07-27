package retrieve

import (
	"path/filepath"
	"strings"
)

// ContextTokens derives query tokens from where the session is working,
// interpreted RELATIVE to the project root.
//
// The naive version — take the basename of the cwd — is a trap: at the
// repo root that basename IS the repo name, a term that appears in a
// large share of the corpus. It then dominates every short prompt and
// the retrieval quietly becomes "show me anything mentioning this repo".
// Measured elsewhere, one such token matched 42 notes on its own.
//
// So: at the root, contribute nothing. Below it, contribute the path
// segments between the root and the cwd — the sub-project or package the
// session is actually in, which is the part that carries signal.
func ContextTokens(projectRoot, cwd string) []string {
	if projectRoot == "" || cwd == "" {
		return nil
	}
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(absRoot, absCwd)
	if err != nil {
		return nil
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return nil // at (or outside) the root: the repo name carries no signal
	}
	out := []string{}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg != "" && seg != "." {
			out = append(out, seg)
		}
	}
	return out
}
