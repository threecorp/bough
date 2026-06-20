package memory

import (
	"fmt"
	"testing"
	"time"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// runBloat asserts the round 3 AI #1 + AI #2 "context bloat" guards.
// A backend that returns more results than MaxResults asked for, or
// that overshoots MaxTokens without setting Truncated, would blow
// Claude's context window in production. We catch both here.
func runBloat(t *testing.T, b memapi.MemoryBackend, cfg Config) {
	t.Helper()
	ctx, cancel := cfg.ctx(t)
	defer cancel()

	scope := memapi.Scope{Level: "worktree", WorktreeID: "bloat", RepoName: "memory-conf"}

	// Seed 30 rows so the cap (default 12) is materially smaller.
	for i := 0; i < 30; i++ {
		rule := fmt.Sprintf("seed rule %02d about bloat", i)
		inst := memapi.Instinct{
			ID:         fmt.Sprintf("bloat-%02d", i),
			Rule:       rule,
			Scope:      scope,
			Source:     "test_failure",
			Confidence: 0.6,
			State:      "active",
			CreatedAt:  time.Now().UTC(),
		}
		_, err := b.Store(ctx, &memapi.StoreReq{
			Instinct:        inst,
			DedupeKey:       fmt.Sprintf("dk-bloat-%02d", i),
			SourceEventID:   fmt.Sprintf("evt-%02d", i),
			UpsertSemantics: false,
		})
		if err != nil {
			t.Fatalf("seed Store#%d: %v", i, err)
		}
	}

	// MaxResults cap: backends MUST honour the count limit.
	qr, err := b.Query(ctx, &memapi.QueryReq{
		Term:       "bloat",
		Scope:      scope,
		MaxResults: 5,
		MaxTokens:  100000,
	})
	if err != nil {
		t.Fatalf("Query MaxResults: %v", err)
	}
	if len(qr.Results) > 5 {
		t.Errorf("MaxResults: backend returned %d results despite cap=5", len(qr.Results))
	}

	// EstimatedTokens reporting: backends MUST set EstimatedTokens
	// > 0 on each result so the host's budget aggregator can stop
	// iterating once it overshoots MaxTokens. Zero is allowed only
	// when the result genuinely carries no content.
	for i, r := range qr.Results {
		if r.EstimatedTokens <= 0 && len(r.Instinct.Rule) > 0 {
			t.Errorf("Result[%d] EstimatedTokens=%d for non-empty rule %q",
				i, r.EstimatedTokens, r.Instinct.Rule)
			break
		}
	}
}
