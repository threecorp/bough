package instinct

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/config"
	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
	"github.com/ikeikeikeike/bough/pkg/schema"
)

// TestCoordinator_Close_NilEvents covers the Round-3 follow-up fix:
// when a Coordinator is constructed without an eventsPath the
// internal events writer is nil; Close must not panic.
func TestCoordinator_Close_NilEvents(t *testing.T) {
	cfg := &config.Config{}
	cfg.Instinct.Enabled = true
	backend := newFakeBackend()
	c, err := New(cfg, backend, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close on nil events: %v", err)
	}
	// Closing twice must also be safe.
	if err := c.Close(); err != nil {
		t.Fatalf("Close (second call): %v", err)
	}
}

// TestConfidencePolicy_Ceiling exercises the source-aware clamp and
// the reinforce delta + ceiling combination.
func TestConfidencePolicy_Ceiling(t *testing.T) {
	policy := NewConfidencePolicy(map[string]float64{
		"explicit_user_feedback": 0.75,
		"llm_only":               0.30,
	}, 0.10)

	cand := &schema.InstinctCandidate{Source: schema.TraceSource("llm_only")}
	policy.ClampInitial(cand, 0.9)
	if cand.Confidence != 0.30 {
		t.Errorf("llm_only ceiling: got %.2f want 0.30", cand.Confidence)
	}

	inst := &schema.Instinct{InstinctCandidate: *cand}
	policy.Reinforce(inst)
	if inst.Confidence != 0.30 {
		t.Errorf("reinforce should not exceed ceiling: got %.2f want 0.30", inst.Confidence)
	}
	if inst.Hits != 1 {
		t.Errorf("hits not bumped: got %d", inst.Hits)
	}

	// Unknown source passes through (no ceiling applies).
	cand2 := &schema.InstinctCandidate{Source: schema.TraceSource("v0.6_new_source")}
	policy.ClampInitial(cand2, 0.8)
	if cand2.Confidence != 0.8 {
		t.Errorf("unknown source ceiling: got %.2f want 0.8", cand2.Confidence)
	}
}

// TestPoisoningGuard_Mode covers the four mint modes including the
// `off` rejection and the explicit `unknown` failure.
func TestPoisoningGuard_Mode(t *testing.T) {
	cand := &schema.InstinctCandidate{Rule: "test rule"}
	cases := []struct {
		mode      string
		wantState schema.InstinctState
		wantErr   bool
	}{
		{"hybrid", schema.InstinctStateCandidate, false},
		{"manual", schema.InstinctStateCandidate, false},
		{"auto-candidate", schema.InstinctStateCandidate, false},
		{"", schema.InstinctStateCandidate, false},
		{"off", schema.InstinctStateUnspecified, true},
		{"unknown-mode", schema.InstinctStateUnspecified, true},
	}
	for _, tc := range cases {
		g := NewPoisoningGuard(tc.mode, true, 200, 14)
		state, err := g.AdmitCandidate(context.Background(), cand)
		if (err != nil) != tc.wantErr {
			t.Errorf("mode=%q err: got %v want err=%v", tc.mode, err, tc.wantErr)
		}
		if state != tc.wantState {
			t.Errorf("mode=%q state: got %q want %q", tc.mode, state, tc.wantState)
		}
	}
}

// TestPoisoningGuard_ActiveCap returns an error once the scope is
// full; zero/negative cap is treated as "no limit".
func TestPoisoningGuard_ActiveCap(t *testing.T) {
	g := NewPoisoningGuard("hybrid", true, 3, 14)
	scope := schema.Scope{Level: schema.ScopeWorktree, WorktreeID: "F-x", RepoName: "auba"}
	if err := g.CheckActiveCap(scope, 2); err != nil {
		t.Errorf("under cap should pass: %v", err)
	}
	if err := g.CheckActiveCap(scope, 3); err == nil {
		t.Errorf("at cap should fail")
	}

	noLimit := NewPoisoningGuard("hybrid", true, 0, 14)
	if err := noLimit.CheckActiveCap(scope, 100); err != nil {
		t.Errorf("no cap should always pass: %v", err)
	}
}

// TestPoisoningGuard_CandidateExpired uses createdAt offsets to
// flip the TTL check.
func TestPoisoningGuard_CandidateExpired(t *testing.T) {
	g := NewPoisoningGuard("hybrid", true, 200, 2)
	now := time.Now()
	fresh := now.Add(-1 * 24 * time.Hour)
	stale := now.Add(-5 * 24 * time.Hour)
	if g.IsCandidateExpired(fresh) {
		t.Errorf("fresh candidate flagged expired")
	}
	if !g.IsCandidateExpired(stale) {
		t.Errorf("stale candidate not flagged expired")
	}
}

// TestBuiltinMinter_PrefersBundleScope is the CRITICAL #5 regression
// test: when a TraceBundle carries its own scope, the minter must
// honour it rather than the batch-level scope argument.
func TestBuiltinMinter_PrefersBundleScope(t *testing.T) {
	bundleScope := schema.Scope{Level: schema.ScopeRepo, RepoName: "auba"}
	batchScope := schema.Scope{Level: schema.ScopeWorktree, WorktreeID: "F-x", RepoName: "auba"}
	bundles := []schema.TraceBundle{
		{
			ID:         "b1",
			Source:     schema.TraceSourceExplicitFeedback,
			Scope:      bundleScope,
			CapturedAt: time.Now().UTC(),
			Content:    "rule from a repo-scoped bundle",
		},
	}
	m := NewBuiltinMinter()
	cands, err := m.Mint(context.Background(), bundles, batchScope)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Scope.Level != schema.ScopeRepo {
		t.Errorf("scope override: got %q want repo", cands[0].Scope.Level)
	}
}

// TestPromote_ValidChain walks each valid promotion (worktree → repo,
// repo → global) and asserts the rejection of every other pair.
func TestPromote_ValidChain(t *testing.T) {
	cases := []struct {
		from    schema.ScopeLevel
		to      schema.ScopeLevel
		wantErr bool
	}{
		{schema.ScopeWorktree, schema.ScopeRepo, false},
		{schema.ScopeRepo, schema.ScopeGlobal, false},
		{schema.ScopeWorktree, schema.ScopeGlobal, true}, // skip the chain
		{schema.ScopeGlobal, schema.ScopeRepo, true},     // backwards
		{schema.ScopeRepo, schema.ScopeRepo, true},       // same tier
	}
	for _, tc := range cases {
		got := validPromotion(tc.from, tc.to)
		if got == tc.wantErr {
			t.Errorf("validPromotion(%s, %s): valid=%v want valid=%v", tc.from, tc.to, got, !tc.wantErr)
		}
	}
}

// TestPromote_BackendPair exercises Promote end-to-end through a
// fake backend: a worktree-scoped instinct must trigger one Store
// at the repo scope and one Forget at the worktree scope.
func TestPromote_BackendPair(t *testing.T) {
	fb := newFakeBackend()
	scope := schema.Scope{Level: schema.ScopeWorktree, WorktreeID: "F-x", RepoName: "auba"}
	inst := schema.Instinct{
		InstinctCandidate: schema.InstinctCandidate{
			ID:        "rule-1",
			Rule:      "promote me",
			Scope:     scope,
			Source:    schema.TraceSourceExplicitFeedback,
			State:     schema.InstinctStateActive,
			DedupeKey: "dk-1",
		},
	}
	if err := Promote(context.Background(), fb, inst, schema.ScopeRepo, nil); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if got := len(fb.stores); got != 1 {
		t.Errorf("Promote should issue 1 Store at target, got %d", got)
	}
	if got := len(fb.forgets); got != 1 {
		t.Errorf("Promote should issue 1 Forget at source, got %d", got)
	}
	if fb.stores[0].Instinct.Scope.Level != "repo" {
		t.Errorf("Store scope: got %q want repo", fb.stores[0].Instinct.Scope.Level)
	}
	if fb.forgets[0].Scope.Level != "worktree" {
		t.Errorf("Forget scope: got %q want worktree", fb.forgets[0].Scope.Level)
	}
}

// TestPromote_InvalidChain asserts the error surface.
func TestPromote_InvalidChain(t *testing.T) {
	fb := newFakeBackend()
	scope := schema.Scope{Level: schema.ScopeWorktree, WorktreeID: "F-x", RepoName: "auba"}
	inst := schema.Instinct{
		InstinctCandidate: schema.InstinctCandidate{
			ID:    "rule-1",
			Rule:  "promote me",
			Scope: scope,
			State: schema.InstinctStateActive,
		},
	}
	if err := Promote(context.Background(), fb, inst, schema.ScopeGlobal, nil); err == nil {
		t.Errorf("Promote worktree → global should error")
	}
}

// TestDecayScheduler_CandidateExpiry asserts that an expired
// candidate is soft-deleted via the backend's Forget RPC.
func TestDecayScheduler_CandidateExpiry(t *testing.T) {
	fb := newFakeBackend()
	expired := schema.Instinct{
		InstinctCandidate: schema.InstinctCandidate{
			ID:        "stale-candidate",
			Rule:      "old idea",
			State:     schema.InstinctStateCandidate,
			CreatedAt: time.Now().Add(-30 * 24 * time.Hour),
		},
	}
	fb.queryResults = []schema.Instinct{expired}
	guard := NewPoisoningGuard("hybrid", true, 200, 7)
	s := NewDecayScheduler(fb, guard, nil, 30)
	forgotten, archived, err := s.RunOnce(context.Background(), schema.Scope{})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if forgotten != 1 {
		t.Errorf("forgotten: got %d want 1", forgotten)
	}
	if archived != 0 {
		t.Errorf("archived: got %d want 0", archived)
	}
	if len(fb.forgets) != 1 {
		t.Errorf("backend Forget calls: got %d want 1", len(fb.forgets))
	}
}

// fakeBackend is a hand-rolled MemoryBackend stand-in for unit tests.
// Records every RPC call so each test asserts the exact backend
// interactions the coordinator issued.
type fakeBackend struct {
	stores       []memapi.StoreReq
	forgets      []memapi.ForgetReq
	queryResults []schema.Instinct
}

func newFakeBackend() *fakeBackend { return &fakeBackend{} }

func (f *fakeBackend) Health(_ context.Context, _ *memapi.HealthReq) (*memapi.HealthResp, error) {
	return &memapi.HealthResp{BackendKind: "fake", PluginVersion: "test"}, nil
}

func (f *fakeBackend) Capabilities(_ context.Context) (*memapi.CapabilitiesResp, error) {
	return &memapi.CapabilitiesResp{PluginVersion: "test"}, nil
}

func (f *fakeBackend) Store(_ context.Context, req *memapi.StoreReq) (*memapi.StoreResp, error) {
	f.stores = append(f.stores, *req)
	return &memapi.StoreResp{StoredID: req.Instinct.ID}, nil
}

func (f *fakeBackend) Query(_ context.Context, _ *memapi.QueryReq) (*memapi.QueryResp, error) {
	out := make([]memapi.QueryResult, 0, len(f.queryResults))
	for _, i := range f.queryResults {
		out = append(out, memapi.QueryResult{
			Instinct: instinctToMemAPI(i),
		})
	}
	return &memapi.QueryResp{Results: out}, nil
}

func (f *fakeBackend) Forget(_ context.Context, req *memapi.ForgetReq) (*memapi.ForgetResp, error) {
	f.forgets = append(f.forgets, *req)
	return &memapi.ForgetResp{}, nil
}

func (f *fakeBackend) Export(_ context.Context, _ *memapi.ExportReq) (*memapi.ExportResp, error) {
	return &memapi.ExportResp{}, nil
}

func (f *fakeBackend) Import(_ context.Context, _ *memapi.ImportReq) (*memapi.ImportResp, error) {
	return &memapi.ImportResp{}, nil
}

// Sanity: ensure errors.New is reachable so future test additions
// have an idiomatic error helper without re-importing.
var _ = errors.New
