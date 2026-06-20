package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// TestWALMode confirms the v0.5 connection setup actually enables
// WAL — the round 3 AI #3 conformance contract pins WAL as
// required for safe concurrency.
func TestWALMode(t *testing.T) {
	p := openTemp(t)
	defer func() { _ = p.Close() }()
	var mode string
	if err := p.db.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode=%q want %q", mode, "wal")
	}
}

func TestStore_Upsert_DedupeKeyMatch(t *testing.T) {
	p := openTemp(t)
	defer func() { _ = p.Close() }()
	ctx := context.Background()
	scope := memapi.Scope{Level: "worktree", WorktreeID: "F-test", RepoName: "auba"}
	inst := memapi.Instinct{
		ID:         "rule-1",
		Rule:       "prefer early returns",
		Scope:      scope,
		Source:     "explicit_user_feedback",
		Confidence: 0.7,
		State:      "active",
		CreatedAt:  time.Now().UTC(),
	}
	// First Store: fresh insert.
	resp1, err := p.Store(ctx, &memapi.StoreReq{
		Instinct: inst, DedupeKey: "dk-1", SourceEventID: "evt-1", UpsertSemantics: true,
	})
	if err != nil {
		t.Fatalf("Store#1: %v", err)
	}
	if resp1.WasUpsert {
		t.Errorf("Store#1 WasUpsert=true on fresh row")
	}
	// Second Store with same DedupeKey but different SourceEventID:
	// matches the dedupe row → upsert.
	resp2, err := p.Store(ctx, &memapi.StoreReq{
		Instinct: inst, DedupeKey: "dk-1", SourceEventID: "evt-2", UpsertSemantics: true,
	})
	if err != nil {
		t.Fatalf("Store#2: %v", err)
	}
	if !resp2.WasUpsert {
		t.Errorf("Store#2 WasUpsert=false on dedupe match")
	}
}

func TestStore_SourceEventIDIdempotency(t *testing.T) {
	p := openTemp(t)
	defer func() { _ = p.Close() }()
	ctx := context.Background()
	scope := memapi.Scope{Level: "worktree", WorktreeID: "F-idemp", RepoName: "auba"}
	inst := memapi.Instinct{
		ID: "rule-2", Rule: "test idempotency", Scope: scope, Source: "test_failure",
		Confidence: 0.6, State: "active", CreatedAt: time.Now().UTC(),
	}
	// First call inserts.
	if _, err := p.Store(ctx, &memapi.StoreReq{Instinct: inst, SourceEventID: "evt-same"}); err != nil {
		t.Fatalf("Store#1: %v", err)
	}
	// Second call with the same SourceEventID returns WasUpsert=true
	// without producing a second row.
	resp, err := p.Store(ctx, &memapi.StoreReq{Instinct: inst, SourceEventID: "evt-same"})
	if err != nil {
		t.Fatalf("Store#2: %v", err)
	}
	if !resp.WasUpsert {
		t.Errorf("Store#2 SourceEventID dedupe: WasUpsert=false (expected idempotent return)")
	}
}

func TestQuery_FTS_Match(t *testing.T) {
	p := openTemp(t)
	defer func() { _ = p.Close() }()
	ctx := context.Background()
	scope := memapi.Scope{Level: "worktree", WorktreeID: "F-fts", RepoName: "auba"}
	for i, rule := range []string{
		"prefer early returns over nested if",
		"avoid magic numbers in tests",
		"use explicit error handling",
	} {
		_, err := p.Store(ctx, &memapi.StoreReq{
			Instinct: memapi.Instinct{
				ID: rule, Rule: rule, Scope: scope, Source: "test_failure",
				Confidence: 0.6, State: "active", CreatedAt: time.Now().UTC(),
			},
			DedupeKey:     rule,
			SourceEventID: rule,
		})
		if err != nil {
			t.Fatalf("seed#%d: %v", i, err)
		}
	}
	qr, err := p.Query(ctx, &memapi.QueryReq{Term: "early", Scope: scope, MaxResults: 5, MaxTokens: 1000})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(qr.Results) != 1 {
		t.Fatalf("Query: expected 1 FTS match, got %d", len(qr.Results))
	}
	if !contains(qr.Results[0].Instinct.Rule, "early") {
		t.Errorf("Query result mismatch: %+v", qr.Results[0].Instinct)
	}
}

func openTemp(t *testing.T) *Provider {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	p, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
