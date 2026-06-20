package mem0

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// This file implements the bough Scope ↔ mem0 namespace mapping
// (Ν-1.1c). The mapping follows docs/NAMESPACE_MAPPING.md plus
// round 4 AI #2's mem0-layered refinement that splits the long-
// lived identity onto user_id and the short-lived per-branch
// identity onto session_id:
//
//	global    →  user_id = global/<user@host>
//	repo      →  user_id = repo/<repo_hash>/<root_hash>
//	worktree  →  user_id = repo/<repo_hash>/<root_hash>
//	             session_id = worktree/<worktree_id>
//
// The repo / worktree split matches mem0's memory-types layering
// (user / session / agent / run) so that two worktrees of the same
// repo share the user namespace but are still queryable per branch.
//
// v0.6 limitation: the wire-side Scope carries only RepoName +
// WorktreeID + Level — there is no remote URL or absolute path
// fingerprint to feed into the hash, so we derive both hashes from
// RepoName today. v0.6.x will extend memapi.Scope with optional
// RemoteFingerprint + RootFingerprint fields so the namespace
// mapping uses sha256(remote URL) and sha256(absolute path)
// exactly as docs/NAMESPACE_MAPPING.md describes. The current
// derivation still cleanly isolates two repos with different names
// and two worktrees of the same repo.

// hashLen is the truncated hex length used for the repo / root
// hash segments. 16 hex chars = 64 bits of collision space, plenty
// for org-scale unique repos and well below the wire-size budget.
const hashLen = 16

// hashShort returns the first hashLen hex chars of sha256(s).
// Returns "" for empty input so the caller can omit the segment.
func hashShort(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:hashLen]
}

// machineID returns a stable identifier for the current host +
// user, used as the `global` scope's namespace fragment. Falls
// back to "unknown-user@unknown-host" when neither is resolvable
// so the caller never has to handle a sentinel error.
func machineID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME") // Windows fallback
	}
	if user == "" {
		user = "unknown-user"
	}
	return fmt.Sprintf("%s@%s", user, host)
}

// scopeKey is the mem0-side (user_id, session_id) pair the
// Provider's Store / Query / Forget paths use. SessionID is empty
// for repo and global scopes.
type scopeKey struct {
	UserID    string
	SessionID string
}

// scopeToMem0 translates a bough Scope into mem0's namespace pair.
// The optional Provider.namespace prefix is prepended so a single
// mem0 organisation can host multiple bough deployments without
// cross-talk.
func (p *Provider) scopeToMem0(s memapi.Scope) scopeKey {
	prefix := ""
	if p.namespace != "" {
		prefix = p.namespace + "/"
	}
	switch s.Level {
	case "global":
		return scopeKey{UserID: prefix + "global/" + machineID()}
	case "repo":
		repoHash := hashShort(s.RepoName)
		rootHash := hashShort(s.RepoName)
		return scopeKey{UserID: prefix + "repo/" + repoHash + "/" + rootHash}
	case "worktree":
		repoHash := hashShort(s.RepoName)
		rootHash := hashShort(s.RepoName + "/" + s.WorktreeID)
		return scopeKey{
			UserID:    prefix + "repo/" + repoHash + "/" + rootHash,
			SessionID: "worktree/" + s.WorktreeID,
		}
	default:
		return scopeKey{UserID: prefix + "unknown/" + strings.ToLower(s.Level)}
	}
}
