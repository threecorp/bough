package schema

// ScopeLevel enumerates the three lifetime tiers an instinct can
// live at. Worktree-scoped instincts disappear when the worktree is
// torn down; repo-scoped instincts survive worktree churn but stay
// tied to the monorepo; global-scoped instincts follow the user
// across monorepos.
type ScopeLevel string

const (
	ScopeUnspecified ScopeLevel = ""
	ScopeWorktree    ScopeLevel = "worktree"
	ScopeRepo        ScopeLevel = "repo"
	ScopeGlobal      ScopeLevel = "global"
)

// Scope addresses one of the three tiers. WorktreeID is the deter-
// ministic ID the host's allocator assigns to a worktree (typically
// the branch name); RepoName is the human-readable monorepo name
// from `.bough.yaml`'s monorepo_root sibling. The coordinator's
// promote.go is the only piece of code that legitimately rewrites
// a Scope; memory backends never invent or change Scope.
//
// Namespace mapping (see docs/NAMESPACE_MAPPING.md): when v0.6
// external backends (mem0 / Graphiti) need to translate a Scope
// into their own user/session/agent triple, the canonical mapping
// is:
//
//	global   → global/<user_or_machine_id>
//	repo     → repo/<repo_remote_hash>/<monorepo_root_hash>
//	worktree → repo/<repo_remote_hash>/worktree/<branch>/<worktree_root_hash>
//
// Mapping branch name alone would collide whenever two repos use
// the same branch name (e.g., `main` or `F-feature` everywhere).
type Scope struct {
	Level      ScopeLevel
	WorktreeID string
	RepoName   string
}

// IsValid returns true if Level is one of the canonical values and
// the discriminator fields are populated consistently with Level.
func (s Scope) IsValid() bool {
	switch s.Level {
	case ScopeWorktree:
		return s.WorktreeID != "" && s.RepoName != ""
	case ScopeRepo:
		return s.RepoName != ""
	case ScopeGlobal:
		return true
	default:
		return false
	}
}
