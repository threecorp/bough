package instinct

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/pkg/schema"
)

// fallbackCfg builds the minimum cfg the coordinator needs to drive
// a Query — Retrieve limits keep the budget loop deterministic.
func fallbackCfg(fallbackOnError bool) *config.Config {
	cfg := &config.Config{}
	cfg.Instinct.Enabled = true
	cfg.Instinct.FallbackOnError = fallbackOnError
	cfg.Instinct.Retrieve.MaxResults = 10
	cfg.Instinct.Retrieve.MaxTokens = 1000
	cfg.Instinct.Retrieve.MinConfidence = 0.0
	return cfg
}

// TestQuery_FallbackOnError_Recovers pins the v0.5.1 MEDIUM #15
// happy path: when the primary backend errors and a fallback is
// wired in with `instinct.fallback_on_error: true`, the coordinator
// replays the same QueryReq against the fallback and returns its
// results.
func TestQuery_FallbackOnError_Recovers(t *testing.T) {
	primary := newFakeBackend()
	primary.queryErr = errors.New("primary boom")
	fallback := newFakeBackend()
	fallback.queryResults = []schema.Instinct{
		{InstinctCandidate: schema.InstinctCandidate{
			ID:   "rule-a",
			Rule: "served from fallback",
		}},
	}
	tmp := t.TempDir() + "/events.jsonl"
	c, err := New(fallbackCfg(true), primary, tmp, fallback)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	out, err := c.Query(context.Background(), "anything",
		schema.Scope{Level: schema.ScopeWorktree, WorktreeID: "F-x", RepoName: "auba"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(out) != 1 || out[0].ID != "rule-a" {
		t.Fatalf("expected fallback row, got %+v", out)
	}
}

// TestQuery_FallbackOnError_Disabled asserts the original behaviour
// holds when the flag is off: a primary failure surfaces without
// touching the fallback (even when one is wired).
func TestQuery_FallbackOnError_Disabled(t *testing.T) {
	primary := newFakeBackend()
	primary.queryErr = errors.New("primary boom")
	fallback := newFakeBackend()
	fallback.queryResults = []schema.Instinct{
		{InstinctCandidate: schema.InstinctCandidate{ID: "should-not-show"}},
	}
	tmp := t.TempDir() + "/events.jsonl"
	c, err := New(fallbackCfg(false), primary, tmp, fallback)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	if _, err := c.Query(context.Background(), "anything",
		schema.Scope{Level: schema.ScopeWorktree, WorktreeID: "F-x", RepoName: "auba"}); err == nil {
		t.Fatalf("expected error when fallback_on_error=false")
	}
}

// TestQuery_FallbackOnError_NilFallback asserts the coordinator
// short-circuits when no fallback backend was wired in — this is
// the v0.5 default (= single SQLite backend, no separate fallback
// process).
func TestQuery_FallbackOnError_NilFallback(t *testing.T) {
	primary := newFakeBackend()
	primary.queryErr = errors.New("primary boom")
	tmp := t.TempDir() + "/events.jsonl"
	c, err := New(fallbackCfg(true), primary, tmp, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	if _, err := c.Query(context.Background(), "anything",
		schema.Scope{Level: schema.ScopeWorktree, WorktreeID: "F-x", RepoName: "auba"}); err == nil {
		t.Fatalf("expected error when fallback backend is nil")
	}
}

// TestQuery_FallbackOnError_BothFail combines a failing primary with
// a failing fallback. The coordinator must surface the primary error
// (wrapped) and mention that the fallback also failed.
func TestQuery_FallbackOnError_BothFail(t *testing.T) {
	primary := newFakeBackend()
	primary.queryErr = errors.New("primary boom")
	fallback := newFakeBackend()
	fallback.queryErr = errors.New("fallback also boom")
	tmp := t.TempDir() + "/events.jsonl"
	c, err := New(fallbackCfg(true), primary, tmp, fallback)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.Query(context.Background(), "anything",
		schema.Scope{Level: schema.ScopeWorktree, WorktreeID: "F-x", RepoName: "auba"})
	if err == nil {
		t.Fatalf("expected error when both backends fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "primary boom") {
		t.Errorf("error should surface primary failure: %q", msg)
	}
	if !strings.Contains(msg, "fallback") {
		t.Errorf("error should mention fallback also failed: %q", msg)
	}
}
