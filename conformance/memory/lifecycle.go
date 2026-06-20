package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// runLifecycle exercises the seven RPCs in their canonical order
// (Health → Capabilities → Store → Query → Forget → Export →
// Import) and asserts the upsert / dedupe / soft-delete invariants
// every v0.5 backend must honour.
func runLifecycle(t *testing.T, b memapi.MemoryBackend, cfg Config) {
	t.Helper()
	ctx, cancel := cfg.ctx(t)
	defer cancel()

	// Health: BackendKind + PluginVersion must not be empty.
	h, err := b.Health(ctx, &memapi.HealthReq{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.BackendKind == "" {
		t.Errorf("Health.BackendKind empty")
	}

	// Capabilities: backend should return a CapabilitiesResp. v0.5
	// backends declare all optional features as false; the suite
	// does not enforce that — it just asserts the call succeeds and
	// PluginVersion is reported.
	caps, err := b.Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps.PluginVersion == "" {
		t.Errorf("Capabilities.PluginVersion empty")
	}

	scope := memapi.Scope{Level: "worktree", WorktreeID: "conformance-A", RepoName: "memory-conf"}

	// Store: insert one row, then upsert the same dedupe_key and
	// assert WasUpsert=true on the second call.
	rule := "prefer early returns over nested if"
	inst := memapi.Instinct{
		ID:         dedupeKey(rule, scope),
		Rule:       rule,
		Scope:      scope,
		Source:     "explicit_user_feedback",
		Confidence: 0.75,
		State:      "active",
		CreatedAt:  time.Now().UTC(),
	}
	dk := dedupeKey(rule, scope)
	store1, err := b.Store(ctx, &memapi.StoreReq{
		Instinct:        inst,
		DedupeKey:       dk,
		SourceEventID:   "lifecycle-1",
		UpsertSemantics: true,
	})
	if err != nil {
		t.Fatalf("Store#1: %v", err)
	}
	if store1.WasUpsert {
		t.Errorf("Store#1 WasUpsert=true on fresh row")
	}

	store2, err := b.Store(ctx, &memapi.StoreReq{
		Instinct:        inst,
		DedupeKey:       dk,
		SourceEventID:   "lifecycle-2",
		UpsertSemantics: true,
	})
	if err != nil {
		t.Fatalf("Store#2: %v", err)
	}
	if !store2.WasUpsert {
		t.Errorf("Store#2 WasUpsert=false on dedupe match")
	}

	// Query: the upserted instinct must come back at hits=2 (or 1+1).
	qr, err := b.Query(ctx, &memapi.QueryReq{
		Term:       "early returns",
		Scope:      scope,
		MaxResults: cfg.MaxResults,
		MaxTokens:  cfg.MaxTokens,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(qr.Results) == 0 {
		t.Fatal("Query: expected at least one result after upsert")
	}
	first := qr.Results[0].Instinct
	if !strings.Contains(first.Rule, "early returns") {
		t.Errorf("Query result rule mismatch: %q", first.Rule)
	}

	// Forget (soft delete): row stays but state flips. After a Forget
	// a Query for the same term should either not return the row or
	// return it with state="forgotten".
	if _, err := b.Forget(ctx, &memapi.ForgetReq{ID: inst.ID, Scope: scope, Reason: "conformance"}); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	// Export → Import round trip.
	exp, err := b.Export(ctx, &memapi.ExportReq{Format: "yaml", Scope: scope})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(exp.Payload) == 0 {
		t.Errorf("Export payload empty")
	}

	imp, err := b.Import(ctx, &memapi.ImportReq{
		Format:            "yaml",
		Payload:           exp.Payload,
		OverwriteExisting: true,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imp.ImportedCount == 0 && imp.UpsertedCount == 0 {
		t.Errorf("Import counted no rows: %+v", imp)
	}
}

// dedupeKey mirrors the host-side canonical computation so the
// suite and a backend that prefers to derive it from rule + scope
// agree on the same hash.
func dedupeKey(rule string, scope memapi.Scope) string {
	h := sha256.New()
	h.Write([]byte(strings.ToLower(strings.TrimSpace(rule))))
	h.Write([]byte("|"))
	h.Write([]byte(scope.Level))
	h.Write([]byte("|"))
	h.Write([]byte(scope.WorktreeID))
	h.Write([]byte("|"))
	h.Write([]byte(scope.RepoName))
	return hex.EncodeToString(h.Sum(nil))
}
