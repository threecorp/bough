// Package instinct is the host-side orchestrator for the v0.5
// instinct subsystem. It sits between the observer pipeline (stdin
// ingest in v0.5 primary path, file watch in opt-in beta) and the
// MemoryBackend plugin client, enforcing the round 1 / round 3
// invariants on every store:
//
//   - Redaction strips PII / secret patterns from raw content.
//   - Confidence is clamped to the source's ceiling.
//   - Poisoning guard refuses duplicates and gates active rows.
//   - Decay archives stale candidates and low-confidence active rows.
//   - Promote moves an instinct between scope tiers as plain
//     Store(target) + Forget(source); backends never own scope.
//   - Audit log records every action to events.jsonl for replay.
//
// The Coordinator is the only entry point the CLI / observer / host
// touch directly. Plugin authors NEVER import this package — their
// surface is plugins/memory/api / plugins/instinct/api.
package instinct

import (
	"context"
	"fmt"
	"time"

	"github.com/ikeikeikeike/bough/internal/config"
	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
	"github.com/ikeikeikeike/bough/pkg/schema"
)

// Coordinator wires the policy layers (redaction, confidence,
// poisoning guard) around a discovered MemoryBackend plugin. It is
// constructed once per CLI invocation; New returns nil and a
// non-empty error if the config is inconsistent.
type Coordinator struct {
	cfg        *config.Config
	backend    memapi.MemoryBackend
	redactor   *Redactor
	confidence *ConfidencePolicy
	guard      *PoisoningGuard
	minter     *BuiltinMinter
	events     *EventWriter

	// fallbackOnError lets the coordinator silently degrade Query
	// to the SQLite reference-fallback when the configured
	// external backend errors. v0.5 only has one backend so the
	// flag is mostly future-proofing for v0.6.
	fallbackOnError bool
}

// New stitches the policy layers together. The caller has already
// discovered the backend through pluginhost; we own the policy.
// eventsPath is typically `<MonorepoRoot>/.bough/memory/events.jsonl`
// — the caller is expected to supply an absolute path so two
// worktrees never race on the same file handle.
func New(cfg *config.Config, backend memapi.MemoryBackend, eventsPath string) (*Coordinator, error) {
	if cfg == nil {
		return nil, fmt.Errorf("instinct.New: cfg is nil")
	}
	if backend == nil {
		return nil, fmt.Errorf("instinct.New: backend is nil")
	}
	r, redactionErrs := NewRedactor(cfg.Instinct.Mint.Redaction.Enabled, cfg.Instinct.Mint.Redaction.PIIPatterns)
	if len(redactionErrs) > 0 {
		// Non-fatal — we just print to stderr and continue. The
		// caller cares because a typo in pii_patterns is more
		// likely than a deliberately-empty redaction list.
		for _, e := range redactionErrs {
			fmt.Println(e) // best-effort surfacing; CLI captures.
		}
	}
	policy := NewConfidencePolicy(cfg.Instinct.Confidence.Sources, cfg.Instinct.Confidence.ReinforceDelta)
	guard := NewPoisoningGuard(
		cfg.Instinct.Mint.Mode,
		cfg.Instinct.Mint.RequireApproval,
		cfg.Instinct.PoisoningGuard.MaxActivePerScope,
		cfg.Instinct.PoisoningGuard.CandidateTTLDays,
	)
	var events *EventWriter
	if eventsPath != "" {
		ew, err := NewEventWriter(eventsPath)
		if err != nil {
			return nil, fmt.Errorf("instinct.New: events writer: %w", err)
		}
		events = ew
	}
	return &Coordinator{
		cfg:             cfg,
		backend:         backend,
		redactor:        r,
		confidence:      policy,
		guard:           guard,
		minter:          NewBuiltinMinter(),
		events:          events,
		fallbackOnError: cfg.Instinct.FallbackOnError,
	}, nil
}

// Close releases the events writer if any. The backend cleanup
// stays with the pluginhost caller — the coordinator does not own
// the plugin subprocess.
//
// Round 3 follow-up: EventWriter.Close is nil-receiver tolerant
// but we add an explicit guard here so a future change to the
// writer surface (e.g. a non-pointer receiver) does not silently
// reintroduce the nil-deref hazard.
func (c *Coordinator) Close() error {
	if c == nil || c.events == nil {
		return nil
	}
	return c.events.Close()
}

// Ingest is the top-level entry the observer pipeline calls with a
// batch of TraceBundles. The flow:
//
//  1. Redact each bundle's content.
//  2. Mint candidates via the built-in minter.
//  3. Apply the confidence ceiling (per-source).
//  4. Run each candidate through the poisoning guard.
//  5. Store the survivors via the memory backend (as upserts).
//  6. Emit events for the audit log.
//
// Returns the number of candidates that survived all gates plus
// the number of upserts the backend reported (= dedupe hits).
func (c *Coordinator) Ingest(ctx context.Context, scope schema.Scope, bundles []schema.TraceBundle) (admitted, reinforced int, err error) {
	if c == nil || c.backend == nil {
		return 0, 0, fmt.Errorf("coordinator: not initialised")
	}
	if !scope.IsValid() {
		return 0, 0, fmt.Errorf("coordinator: invalid scope %+v", scope)
	}
	sanitised := make([]schema.TraceBundle, len(bundles))
	for i, b := range bundles {
		sanitised[i] = c.redactor.Sanitise(b)
	}
	candidates, err := c.minter.Mint(ctx, sanitised, scope)
	if err != nil {
		return 0, 0, fmt.Errorf("coordinator: mint: %w", err)
	}
	for _, cand := range candidates {
		c.confidence.ClampInitial(cand, cand.Confidence)
		state, gateErr := c.guard.AdmitCandidate(ctx, cand)
		if gateErr != nil {
			continue
		}
		cand.State = state
		store, storeErr := c.backend.Store(ctx, &memapi.StoreReq{
			Instinct:        candidateToInstinctAPI(cand),
			DedupeKey:       cand.DedupeKey,
			SourceEventID:   firstTrace(cand.SourceTraces),
			UpsertSemantics: true,
		})
		if storeErr != nil {
			_ = c.events.Append(Event{Kind: "store_error", ID: cand.ID, Detail: storeErr.Error()})
			continue
		}
		if store.WasUpsert {
			reinforced++
		}
		admitted++
		_ = c.events.Append(Event{
			Kind:   "mint",
			Scope:  fmt.Sprintf("%s/%s/%s", cand.Scope.Level, cand.Scope.WorktreeID, cand.Scope.RepoName),
			ID:     store.StoredID,
			Detail: fmt.Sprintf("source=%s confidence=%.2f upsert=%v", cand.Source, cand.Confidence, store.WasUpsert),
		})
	}
	return admitted, reinforced, nil
}

// Approve flips a candidate row to active. The caller (CLI) has
// already validated that the ID exists; the coordinator just
// drives the backend's upsert.
func (c *Coordinator) Approve(ctx context.Context, id string, scope schema.Scope) error {
	// We do not currently have a backend method to read by ID; the
	// approval is expressed as a Store with state="active". For
	// v0.5 the CLI passes the full Instinct it already has.
	return fmt.Errorf("coordinator.Approve: caller must pass the resolved Instinct; use ApproveInstinct")
}

// ApproveInstinct upserts the given Instinct with state="active".
// The CLI is responsible for fetching the row first (via Query)
// and then handing it back.
func (c *Coordinator) ApproveInstinct(ctx context.Context, inst schema.Instinct) error {
	inst.State = schema.InstinctStateActive
	inst.LastHitAt = time.Now().UTC()
	_, err := c.backend.Store(ctx, &memapi.StoreReq{
		Instinct:        instinctToMemAPI(inst),
		DedupeKey:       inst.DedupeKey,
		SourceEventID:   "approve/" + inst.ID,
		UpsertSemantics: true,
	})
	if err != nil {
		return fmt.Errorf("coordinator.Approve: %w", err)
	}
	_ = c.events.Append(Event{Kind: "approve", ID: inst.ID})
	return nil
}

// Query routes to the backend and applies the host-side budget cap
// (round 3 AI #1). The backend is supposed to respect MaxResults
// and MaxTokens, but the coordinator is the source of truth — if
// the backend over-returns, we trim here.
func (c *Coordinator) Query(ctx context.Context, term string, scope schema.Scope) ([]schema.Instinct, error) {
	resp, err := c.backend.Query(ctx, &memapi.QueryReq{
		Term:          term,
		Scope:         scopeToMemAPI(scope),
		MaxResults:    c.cfg.Instinct.Retrieve.MaxResults,
		MaxTokens:     c.cfg.Instinct.Retrieve.MaxTokens,
		MinConfidence: c.cfg.Instinct.Retrieve.MinConfidence,
	})
	if err != nil {
		return nil, fmt.Errorf("coordinator.Query: %w", err)
	}
	budget := &schema.RetrieveBudget{
		MaxResults: c.cfg.Instinct.Retrieve.MaxResults,
		MaxTokens:  c.cfg.Instinct.Retrieve.MaxTokens,
	}
	out := make([]schema.Instinct, 0, len(resp.Results))
	for _, r := range resp.Results {
		if !budget.Allow(r.EstimatedTokens, r.Truncated) {
			break
		}
		budget.Consume(r.EstimatedTokens, r.Truncated)
		out = append(out, memInstinctToSchema(r.Instinct))
	}
	_ = c.events.Append(Event{
		Kind:   "query",
		Detail: fmt.Sprintf("term=%q results=%d/%d tokens=%d truncated=%v", term, len(out), len(resp.Results), budget.UsedTokens, budget.AnyTruncated),
	})
	return out, nil
}

// Forget calls the backend's soft-delete and emits an audit event.
func (c *Coordinator) Forget(ctx context.Context, id string, scope schema.Scope, reason string) error {
	if _, err := c.backend.Forget(ctx, &memapi.ForgetReq{
		ID:     id,
		Scope:  scopeToMemAPI(scope),
		Reason: reason,
	}); err != nil {
		return fmt.Errorf("coordinator.Forget: %w", err)
	}
	_ = c.events.Append(Event{Kind: "forget", ID: id, Detail: reason})
	return nil
}

// Promote delegates to the package-level Promote helper. The flow
// (Store at target + Forget at source) is identical for every
// backend; centralising it here means a v0.6 mem0 plugin and the
// SQLite reference-fallback move data identically.
func (c *Coordinator) Promote(ctx context.Context, inst schema.Instinct, target schema.ScopeLevel) error {
	return Promote(ctx, c.backend, inst, target, c.events)
}

// candidateToInstinctAPI lifts a schema.InstinctCandidate to the
// memapi.Instinct wire shape. The freshly-minted row carries Hits=0
// / no EvidenceRefs / no LastHitAt.
func candidateToInstinctAPI(c *schema.InstinctCandidate) memapi.Instinct {
	return memapi.Instinct{
		ID:           c.ID,
		Rule:         c.Rule,
		Why:          c.Why,
		HowToApply:   c.HowToApply,
		Domain:       c.Domain,
		Scope:        scopeToMemAPI(c.Scope),
		Source:       string(c.Source),
		Confidence:   c.Confidence,
		State:        string(c.State),
		CreatedAt:    c.CreatedAt,
		SourceTraces: c.SourceTraces,
		DedupeKey:    c.DedupeKey,
	}
}

// memInstinctToSchema is the inverse of instinctToMemAPI — the
// wire row coming back from a backend Query lands here.
func memInstinctToSchema(i memapi.Instinct) schema.Instinct {
	out := schema.Instinct{
		InstinctCandidate: schema.InstinctCandidate{
			ID:           i.ID,
			Rule:         i.Rule,
			Why:          i.Why,
			HowToApply:   i.HowToApply,
			Domain:       i.Domain,
			Scope:        schema.Scope{Level: schema.ScopeLevel(i.Scope.Level), WorktreeID: i.Scope.WorktreeID, RepoName: i.Scope.RepoName},
			Source:       schema.TraceSource(i.Source),
			Confidence:   i.Confidence,
			State:        schema.InstinctState(i.State),
			SourceTraces: i.SourceTraces,
			CreatedAt:    i.CreatedAt,
			DedupeKey:    i.DedupeKey,
		},
		Hits:         i.Hits,
		LastHitAt:    i.LastHitAt,
		EvidenceRefs: i.EvidenceRefs,
	}
	if i.MetadataJSON != "" {
		out.Metadata = []byte(i.MetadataJSON)
	}
	return out
}

func firstTrace(traces []string) string {
	if len(traces) == 0 {
		return ""
	}
	return traces[0]
}
