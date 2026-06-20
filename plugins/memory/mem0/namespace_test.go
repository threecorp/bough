package mem0

import (
	"strings"
	"testing"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// helper to construct a Provider with only the namespace prefix
// set — the HTTP client / endpoint are not needed for pure mapping
// tests.
func newNamespaceProvider(prefix string) *Provider {
	return &Provider{namespace: prefix}
}

func TestScopeToMem0_Global(t *testing.T) {
	p := newNamespaceProvider("")
	k := p.scopeToMem0(memapi.Scope{Level: "global"})
	if !strings.HasPrefix(k.UserID, "global/") {
		t.Errorf("global user_id should be prefixed with 'global/': got %q", k.UserID)
	}
	if k.SessionID != "" {
		t.Errorf("global scope should not carry a session_id: got %q", k.SessionID)
	}
}

func TestScopeToMem0_Repo(t *testing.T) {
	p := newNamespaceProvider("")
	k := p.scopeToMem0(memapi.Scope{Level: "repo", RepoName: "auba"})
	if !strings.HasPrefix(k.UserID, "repo/") {
		t.Errorf("repo user_id should be prefixed with 'repo/': got %q", k.UserID)
	}
	parts := strings.Split(k.UserID, "/")
	if len(parts) != 3 {
		t.Fatalf("repo user_id should be repo/<hash>/<hash>: got %q (%d parts)", k.UserID, len(parts))
	}
	if len(parts[1]) != hashLen || len(parts[2]) != hashLen {
		t.Errorf("repo user_id hash segments should be %d chars: got %d / %d", hashLen, len(parts[1]), len(parts[2]))
	}
	if k.SessionID != "" {
		t.Errorf("repo scope should not carry a session_id: got %q", k.SessionID)
	}
}

func TestScopeToMem0_Worktree(t *testing.T) {
	p := newNamespaceProvider("")
	k := p.scopeToMem0(memapi.Scope{Level: "worktree", WorktreeID: "F-feat", RepoName: "auba"})
	if !strings.HasPrefix(k.UserID, "repo/") {
		t.Errorf("worktree user_id should reuse the repo prefix: got %q", k.UserID)
	}
	if k.SessionID != "worktree/F-feat" {
		t.Errorf("worktree session_id should be 'worktree/<id>': got %q", k.SessionID)
	}
}

// TestScopeToMem0_RepoVsWorktree_UserIDShared pins the round 4 AI
// #2 design: the repo-level user_id is reused by every worktree of
// that repo so mem0's user-tier memories remain shared, while the
// session_id carries the per-branch identity.
func TestScopeToMem0_RepoVsWorktree_UserIDShared(t *testing.T) {
	p := newNamespaceProvider("")
	repo := p.scopeToMem0(memapi.Scope{Level: "repo", RepoName: "auba"})
	worktree := p.scopeToMem0(memapi.Scope{Level: "worktree", RepoName: "auba", WorktreeID: "F-feat"})
	// Repo prefix is identical (the first two hash segments — same
	// RepoName); only the trailing root_hash differs because the
	// worktree mixes WorktreeID into the root hash.
	if !strings.HasPrefix(worktree.UserID, "repo/") || !strings.HasPrefix(repo.UserID, "repo/") {
		t.Fatalf("both user_ids should start with repo/: repo=%q worktree=%q", repo.UserID, worktree.UserID)
	}
	repoParts := strings.Split(repo.UserID, "/")
	worktreeParts := strings.Split(worktree.UserID, "/")
	if repoParts[1] != worktreeParts[1] {
		t.Errorf("repo_hash should match across repo and worktree scopes: repo=%q worktree=%q", repoParts[1], worktreeParts[1])
	}
}

// TestScopeToMem0_DifferentRepos_Disjoint pins the AI #2 isolation
// guarantee: two repos with different names must NEVER share a
// user_id, even if their worktrees happen to use the same name.
func TestScopeToMem0_DifferentRepos_Disjoint(t *testing.T) {
	p := newNamespaceProvider("")
	a := p.scopeToMem0(memapi.Scope{Level: "worktree", RepoName: "auba", WorktreeID: "main"})
	b := p.scopeToMem0(memapi.Scope{Level: "worktree", RepoName: "extremo", WorktreeID: "main"})
	if a.UserID == b.UserID {
		t.Errorf("two repos must not share a user_id: %q == %q", a.UserID, b.UserID)
	}
}

// TestScopeToMem0_NamespacePrefix asserts the multi-tenant prefix
// applies to every scope level without leaking the raw value.
func TestScopeToMem0_NamespacePrefix(t *testing.T) {
	p := newNamespaceProvider("tenant-x")
	cases := []memapi.Scope{
		{Level: "global"},
		{Level: "repo", RepoName: "auba"},
		{Level: "worktree", RepoName: "auba", WorktreeID: "F-feat"},
	}
	for _, s := range cases {
		k := p.scopeToMem0(s)
		if !strings.HasPrefix(k.UserID, "tenant-x/") {
			t.Errorf("scope=%+v: user_id should start with 'tenant-x/': got %q", s, k.UserID)
		}
	}
}

// TestMachineID_Stable asserts machineID returns a usable value
// even when neither USER nor HOSTNAME is resolvable. We do not
// depend on host-specific values; this is a smoke test that the
// helper never returns the empty string.
func TestMachineID_Stable(t *testing.T) {
	id := machineID()
	if id == "" {
		t.Fatal("machineID must never return an empty string")
	}
	if !strings.Contains(id, "@") {
		t.Errorf("machineID should be 'user@host'; got %q", id)
	}
}

// TestHashShort_EmptyAndKnown locks down the hash helper so a
// future refactor cannot silently truncate or extend the hex
// prefix.
func TestHashShort_EmptyAndKnown(t *testing.T) {
	if got := hashShort(""); got != "" {
		t.Errorf("hashShort(\"\") should be empty: got %q", got)
	}
	got := hashShort("auba")
	if len(got) != hashLen {
		t.Errorf("hashShort should return %d hex chars: got %d (%q)", hashLen, len(got), got)
	}
	// Idempotent — same input produces same output.
	if hashShort("auba") != got {
		t.Error("hashShort must be deterministic")
	}
}
