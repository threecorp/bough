package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// TestStore_DedupeWithoutUpsertReturnsError pins the CRITICAL #1
// follow-up fix: the previous INSERT OR REPLACE fall-through could
// silently destroy an existing row when the caller forgot to set
// UpsertSemantics. We now reject the call explicitly.
func TestStore_DedupeWithoutUpsertReturnsError(t *testing.T) {
	p := openTemp(t)
	defer p.Close()
	ctx := context.Background()
	scope := memapi.Scope{Level: "worktree", WorktreeID: "F-rev", RepoName: "auba"}
	inst := memapi.Instinct{
		ID:         "rev-1",
		Rule:       "regression rule",
		Scope:      scope,
		Source:     "explicit_user_feedback",
		Confidence: 0.7,
		State:      "active",
		CreatedAt:  time.Now().UTC(),
	}
	// Seed.
	if _, err := p.Store(ctx, &memapi.StoreReq{Instinct: inst, DedupeKey: "dk-rev"}); err != nil {
		t.Fatalf("seed Store: %v", err)
	}
	// Reattempt with same dedupe key but UpsertSemantics=false.
	_, err := p.Store(ctx, &memapi.StoreReq{Instinct: inst, DedupeKey: "dk-rev", UpsertSemantics: false})
	if err == nil {
		t.Fatalf("expected error on dedupe match without UpsertSemantics; got nil")
	}
	if !strings.Contains(err.Error(), "dedupe match") {
		t.Errorf("error should describe the dedupe collision; got %q", err.Error())
	}
}

// TestStore_UpsertUpdatesState exercises the CRITICAL #4 follow-up:
// the reinforce path now propagates the incoming state so the decay
// scheduler can transition an active row to archived through the
// same Store RPC.
func TestStore_UpsertUpdatesState(t *testing.T) {
	p := openTemp(t)
	defer p.Close()
	ctx := context.Background()
	scope := memapi.Scope{Level: "worktree", WorktreeID: "F-rev", RepoName: "auba"}
	inst := memapi.Instinct{
		ID:         "rev-2",
		Rule:       "decay regression",
		Scope:      scope,
		Source:     "test_failure",
		Confidence: 0.6,
		State:      "active",
		CreatedAt:  time.Now().UTC(),
	}
	if _, err := p.Store(ctx, &memapi.StoreReq{Instinct: inst, DedupeKey: "dk-rev-2"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Upsert with state=archived.
	inst.State = "archived"
	if _, err := p.Store(ctx, &memapi.StoreReq{Instinct: inst, DedupeKey: "dk-rev-2", UpsertSemantics: true}); err != nil {
		t.Fatalf("archive upsert: %v", err)
	}
	// Query should not return the row (state filter excludes
	// 'archived' rows from term search). We use the no-term path
	// which includes archived; verify state changed.
	resp, err := p.Query(ctx, &memapi.QueryReq{Term: "", Scope: scope, MaxResults: 10, MaxTokens: 1000})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	var found bool
	for _, r := range resp.Results {
		if r.Instinct.ID == "rev-2" {
			found = true
			if r.Instinct.State != "archived" {
				t.Errorf("state not transitioned: got %q want archived", r.Instinct.State)
			}
		}
	}
	if !found {
		t.Fatalf("archived row missing from no-term Query")
	}
}

// TestQuery_FTSNormalisation is the CRITICAL #2 regression: every
// known FTS5 metasyntax character is stripped before the term is
// wrapped in a phrase quote, so a malicious user term cannot break
// out of the phrase and inject NEAR / column filters / wildcards.
func TestQuery_FTSNormalisation(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{`early returns`, `early returns`},
		{`early "returns"`, `early returns`},
		{`NEAR(rule:foo, bar)`, `NEARrulefoo bar`},
		{`foo* OR rule:bar`, `foo OR rulebar`},
		{`x\x00y`, `xx00y`},
		// Unicode quotes are stripped.
		{"“early” returns", "early returns"},
		// Punctuation reduced to empty → fallback to scope-only path.
		{`***`, ``},
	}
	for _, tc := range cases {
		got := normalizeFTSTerm(tc.input)
		if got != tc.want {
			t.Errorf("normalizeFTSTerm(%q): got %q want %q", tc.input, got, tc.want)
		}
	}
}

// TestQuery_MaxTokensDoesNotExceedCap is the HIGH #7 regression:
// when MaxTokens would be breached by the next result, the backend
// stops iteration WITHOUT including that result. The previous
// semantics included a Truncated=true row whose tokens pushed
// runningTok past the cap.
func TestQuery_MaxTokensDoesNotExceedCap(t *testing.T) {
	p := openTemp(t)
	defer p.Close()
	ctx := context.Background()
	scope := memapi.Scope{Level: "worktree", WorktreeID: "F-cap", RepoName: "auba"}
	for i := 0; i < 10; i++ {
		inst := memapi.Instinct{
			ID:         "cap-" + string(rune('a'+i)),
			Rule:       "a fairly long rule with many tokens to exercise the cap path",
			Scope:      scope,
			Source:     "test_failure",
			Confidence: 0.6,
			State:      "active",
			CreatedAt:  time.Now().UTC(),
		}
		if _, err := p.Store(ctx, &memapi.StoreReq{
			Instinct:  inst,
			DedupeKey: inst.ID,
		}); err != nil {
			t.Fatalf("seed#%d: %v", i, err)
		}
	}
	resp, err := p.Query(ctx, &memapi.QueryReq{
		Term:       "",
		Scope:      scope,
		MaxResults: 20,
		MaxTokens:  20, // tiny cap: 1-2 rows max
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	total := 0
	for _, r := range resp.Results {
		total += r.EstimatedTokens
	}
	if total > 20 {
		t.Errorf("MaxTokens cap breached: total=%d cap=20 results=%d", total, len(resp.Results))
	}
}
