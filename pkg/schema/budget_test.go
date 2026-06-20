package schema

import "testing"

// TestRetrieveBudget_Allow_resultCap verifies the result-count cap
// is enforced before the token cap.
func TestRetrieveBudget_Allow_resultCap(t *testing.T) {
	b := &RetrieveBudget{MaxResults: 2, MaxTokens: 100000}
	if !b.Allow(10, false) {
		t.Fatalf("Allow result 1 should fit: %+v", b)
	}
	b.Consume(10, false)
	if !b.Allow(10, false) {
		t.Fatalf("Allow result 2 should fit: %+v", b)
	}
	b.Consume(10, false)
	if b.Allow(10, false) {
		t.Fatalf("Allow result 3 should be denied (cap=2): %+v", b)
	}
}

// TestRetrieveBudget_Allow_tokenCap verifies the token cap is
// enforced when results would exceed the running total.
func TestRetrieveBudget_Allow_tokenCap(t *testing.T) {
	b := &RetrieveBudget{MaxResults: 100, MaxTokens: 50}
	if !b.Allow(30, false) {
		t.Fatalf("Allow 30 tokens should fit: %+v", b)
	}
	b.Consume(30, false)
	if !b.Allow(20, false) {
		t.Fatalf("Allow 20 more (=50) should fit at cap: %+v", b)
	}
	b.Consume(20, false)
	if b.Allow(1, false) {
		t.Fatalf("Allow 1 more should be denied (over cap): %+v", b)
	}
}

// TestRetrieveBudget_AnyTruncated verifies the truncated flag rolls
// up into the budget for the post-query summary.
func TestRetrieveBudget_AnyTruncated(t *testing.T) {
	b := &RetrieveBudget{MaxResults: 10, MaxTokens: 1000}
	b.Consume(10, false)
	if b.AnyTruncated {
		t.Fatalf("AnyTruncated should stay false until a truncated result lands")
	}
	b.Consume(20, true)
	if !b.AnyTruncated {
		t.Fatalf("AnyTruncated should latch true after a truncated result")
	}
	// A subsequent untruncated result must not clear the latch.
	b.Consume(5, false)
	if !b.AnyTruncated {
		t.Fatalf("AnyTruncated should stay true once latched")
	}
}

// TestScope_IsValid covers the three valid combinations and a few
// invalid ones to catch field-population regressions.
func TestScope_IsValid(t *testing.T) {
	cases := []struct {
		name string
		s    Scope
		want bool
	}{
		{"worktree happy", Scope{Level: ScopeWorktree, WorktreeID: "F-feature", RepoName: "auba"}, true},
		{"worktree missing repo", Scope{Level: ScopeWorktree, WorktreeID: "F-feature"}, false},
		{"worktree missing id", Scope{Level: ScopeWorktree, RepoName: "auba"}, false},
		{"repo happy", Scope{Level: ScopeRepo, RepoName: "auba"}, true},
		{"repo missing name", Scope{Level: ScopeRepo}, false},
		{"global", Scope{Level: ScopeGlobal}, true},
		{"unspecified", Scope{}, false},
	}
	for _, tc := range cases {
		if got := tc.s.IsValid(); got != tc.want {
			t.Errorf("%s: IsValid=%v want %v", tc.name, got, tc.want)
		}
	}
}
