// Package mem0 is the bough memory plugin for mem0
// (https://github.com/mem0ai/mem0). It speaks mem0's HTTP REST API
// for Store / Query / Forget / Export / Import and translates
// bough's Scope into mem0's user_id / session_id namespace.
//
// bough is not an agent memory system. bough is a per-worktree
// memory orchestration layer. mem0 is the memory engine; this
// plugin is the adapter the host coordinator routes through.
//
// Round 4 review notes wired in here:
//
//   - AI #1 + #2: Read fallback only. Store does NOT fall back to
//     the SQLite reference-fallback on error — that is what causes
//     the "split-brain" hazard the synthesis flagged as Blocker 1.
//     The host coordinator enforces this by consulting
//     `instinct.fallback_on_error` only from Query.
//
//   - AI #2: namespace mapping = repo → user_id, worktree →
//     session_id. The repo identity is the long-lived axis; the
//     worktree identity is the short-lived per-branch axis.
//
//   - AI #1 + #2: short-TTL read-through cache for Query, evicted
//     on any successful Store / Forget / Import.
//
//   - AI #2: advertise the v0.6 Capabilities cluster mem0 actually
//     supports (SemanticQuery / VectorSearch / NamespaceIsolation /
//     TTL / EventualConsistency / MetadataFilter / BulkImport).
//
// v0.6.0-dev: this file ships the skeleton — Provider struct,
// Capabilities, Health, and stubbed Store / Query / Forget / Export
// / Import that all return errNotYetImplemented. The HTTP client
// adapter (Ν-1.1d), namespace mapping (Ν-1.1c), and cache layer
// (Ν-1.1e) land in subsequent commits.
package mem0

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// Version is reported through Health and Capabilities. Bumped per
// release; the v0.6.0 ship commit replaces the -dev suffix.
const Version = "v0.6.0-dev"

// Provider is the bough-side handle to a mem0 instance. The HTTP
// client is configurable so tests can swap in an httptest.Server.
type Provider struct {
	client    *http.Client
	endpoint  string        // mem0 base URL, e.g. https://api.mem0.ai
	apiKey    string        // mem0 organisation / API key, empty for self-hosted
	namespace string        // optional prefix injected into every user_id (multi-tenant routing)
	timeout   time.Duration // HTTP request timeout
}

// Config carries the values the plugin entry binary reads from
// environment variables before constructing the Provider. Keeping
// it a separate struct lets the binary stay tiny while making
// dependency injection for tests obvious.
type Config struct {
	Endpoint  string
	APIKey    string
	Namespace string
	Timeout   time.Duration
}

// New constructs a Provider. v0.6 skeleton wires the HTTP client
// + config only; per-method implementations land in Ν-1.1d. The
// endpoint is required so a misconfigured `.bough.yaml` fails at
// Discover time rather than silently hitting localhost.
func New(cfg Config) (*Provider, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("mem0 plugin: endpoint is empty (set BOUGH_MEMORY_MEM0_ENDPOINT in .bough.yaml memory_backends[*])")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &Provider{
		client:    &http.Client{Timeout: cfg.Timeout},
		endpoint:  cfg.Endpoint,
		apiKey:    cfg.APIKey,
		namespace: cfg.Namespace,
		timeout:   cfg.Timeout,
	}, nil
}

// Close releases resources held by the Provider. The http.Client
// does not require explicit close; the receiver is kept for parity
// with the sqlite reference-fallback so the host's lifecycle code
// can treat both backends identically.
func (p *Provider) Close() error {
	return nil
}

// Health probes mem0's reachability. v0.6 skeleton returns static
// plugin metadata; the real probe (= GET <endpoint>/health) lands
// in Ν-1.1d.
func (p *Provider) Health(_ context.Context, _ *memapi.HealthReq) (*memapi.HealthResp, error) {
	return &memapi.HealthResp{BackendKind: "mem0", PluginVersion: Version}, nil
}

// Capabilities advertises mem0's feature set against the v0.6
// 17-field CapabilitiesResp. The mem0-strengths cluster
// (SemanticQuery / VectorSearch / NamespaceIsolation / TTL /
// EventualConsistency / MetadataFilter / BulkImport / BulkExport)
// is lit up; DedupeKey / SourceEventID stay false because mem0
// itself has no native dedupe primitive — the host computes
// idempotency tokens and the SQLite reference-fallback owns that
// contract during Read fallback.
//
// GraphQuery + TemporalQuery stay false: those land with the
// Graphiti plugin (= Ν-1.3 docs + skeleton, full implementation
// deferred to v0.6.x per round 4 AI #2).
func (p *Provider) Capabilities(_ context.Context) (*memapi.CapabilitiesResp, error) {
	return &memapi.CapabilitiesResp{
		// v0.5
		SemanticQuery:    true,
		GraphQuery:       false,
		BulkExport:       true,
		VectorSearch:     true,
		SupportsMetadata: true,
		PluginVersion:    Version,
		// v0.6 (= round 4 priority A12)
		TemporalQuery:       false,
		MetadataFilter:      true,
		NamespaceIsolation:  true,
		SoftDelete:          true,
		BulkImport:          true,
		DedupeKey:           false,
		SourceEventID:       false,
		TTL:                 true,
		EventualConsistency: true,
		MaxBatchSize:        0,
		MaxQueryTokens:      0,
	}, nil
}

// errNotYetImplemented is the stub sentinel every per-RPC method
// returns until Ν-1.1d wires the actual HTTP client. Keeping it
// exported via errors.Is lets the conformance suite and unit tests
// assert on the stub state without depending on the error string.
var errNotYetImplemented = errors.New("mem0 plugin: not yet implemented (Ν-1.1d)")

// Store routes the upsert through mem0's add-memory endpoint.
// Round 4 AI #1 + #2: Store does NOT fall back to the SQLite
// reference-fallback on error. That is the host coordinator's
// guarantee: fallback_on_error is consulted only from Query.
// v0.6 skeleton: stub.
func (p *Provider) Store(_ context.Context, req *memapi.StoreReq) (*memapi.StoreResp, error) {
	return nil, fmt.Errorf("Store: %w (instinct=%s)", errNotYetImplemented, req.Instinct.ID)
}

// Query searches mem0 for matching instincts. The host's Read
// fallback path consults this method; on error the coordinator can
// replay against the SQLite reference-fallback (= v0.5.1 wire).
// v0.6 skeleton: stub.
func (p *Provider) Query(_ context.Context, _ *memapi.QueryReq) (*memapi.QueryResp, error) {
	return nil, fmt.Errorf("Query: %w", errNotYetImplemented)
}

// Forget soft-deletes an instinct. mem0 implements this by deleting
// the underlying memory; the SoftDelete capability is still honest
// because the row simply stops being queryable, which is what the
// host coordinator's audit log treats as "forgotten".
// v0.6 skeleton: stub.
func (p *Provider) Forget(_ context.Context, req *memapi.ForgetReq) (*memapi.ForgetResp, error) {
	return nil, fmt.Errorf("Forget: %w (id=%s)", errNotYetImplemented, req.ID)
}

// Export walks mem0's bulk-export endpoint. v0.6 skeleton: stub.
func (p *Provider) Export(_ context.Context, _ *memapi.ExportReq) (*memapi.ExportResp, error) {
	return nil, fmt.Errorf("Export: %w", errNotYetImplemented)
}

// Import replays previously-exported memories into mem0. Mirrors
// the round-trip semantics sqlite gained in v0.5.1.
// v0.6 skeleton: stub.
func (p *Provider) Import(_ context.Context, _ *memapi.ImportReq) (*memapi.ImportResp, error) {
	return nil, fmt.Errorf("Import: %w", errNotYetImplemented)
}
