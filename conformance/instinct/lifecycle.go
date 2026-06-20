package instinct

import (
	"testing"
	"time"

	instapi "github.com/ikeikeikeike/bough/plugins/instinct/api"
)

// runLifecycle exercises the single Mint RPC across two scenarios:
//   - a bundle that should yield at least one candidate,
//   - an empty bundle that may (validly) yield no candidates.
// The suite asserts the round 3 AI #1 invariant that MintResp.Candidates
// is a plural slice (never collapsed to a single max-confidence pick).
func runLifecycle(t *testing.T, m instapi.InstinctMinter, cfg Config) {
	t.Helper()
	ctx, cancel := cfg.ctx(t)
	defer cancel()

	scope := instapi.Scope{Level: "worktree", WorktreeID: "conformance-A", RepoName: "instinct-conf"}

	bundles := []instapi.TraceBundle{
		{
			ID:            "trace-1",
			Source:        "test_failure",
			Scope:         scope,
			CapturedAt:    time.Now().UTC(),
			Content:       "TestEarlyReturn failed: nested conditional should use early return",
			EvidenceRef:   "session-A",
			SourceEventID: "evt-1",
		},
		{
			ID:            "trace-2",
			Source:        "test_failure",
			Scope:         scope,
			CapturedAt:    time.Now().UTC(),
			Content:       "TestErrorHandling failed: silently swallowed err; surface it",
			EvidenceRef:   "session-A",
			SourceEventID: "evt-2",
		},
	}

	resp, err := m.Mint(ctx, &instapi.MintReq{TraceBundles: bundles, Scope: scope})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if resp == nil {
		t.Fatal("Mint: nil response")
	}
	// Plurality invariant: minters may return zero candidates for a
	// weak bundle, but the response must be a slice (never collapsed
	// to a "one best" single).
	if resp.Candidates == nil {
		t.Errorf("Mint.Candidates: nil slice (expected non-nil; empty is fine)")
	}
	for i, c := range resp.Candidates {
		if c == nil {
			t.Errorf("Mint.Candidates[%d] is nil", i)
			continue
		}
		if c.Rule == "" {
			t.Errorf("Mint.Candidates[%d].Rule empty", i)
		}
		if c.State != "" && c.State != "candidate" {
			t.Errorf("Mint.Candidates[%d].State=%q (expected empty or \"candidate\")", i, c.State)
		}
		if c.DedupeKey == "" {
			t.Errorf("Mint.Candidates[%d].DedupeKey empty", i)
		}
	}

	// Empty bundle: plugins MUST handle gracefully (no panic, no
	// error) and return an empty candidate slice.
	respEmpty, err := m.Mint(ctx, &instapi.MintReq{TraceBundles: nil, Scope: scope})
	if err != nil {
		t.Fatalf("Mint empty: %v", err)
	}
	if respEmpty == nil {
		t.Fatal("Mint empty: nil response")
	}
	if len(respEmpty.Candidates) != 0 {
		t.Errorf("Mint empty: expected 0 candidates, got %d", len(respEmpty.Candidates))
	}
}
