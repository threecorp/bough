// Package api carries the host-↔-plugin contract for bough memory
// backend plugins. Plugins implement MemoryBackend over Hashicorp
// go-plugin's gRPC transport; the host and the plugin both link this
// package. Generated gRPC stubs live under api/proto.
//
// bough is not an agent memory system. bough is a per-worktree memory
// orchestration layer. The MemoryBackend interface is intentionally
// limited to persistence concerns (Health, Capabilities, Store,
// Query, Forget, Export, Import). Minting candidates from raw traces
// belongs to InstinctMinter (plugins/instinct/api). Compiling
// approved instincts into reusable skills/commands/tools/agents
// belongs to CapabilityCompiler (plugins/capability/api). Scoring
// compiled artifacts belongs to SkillEvaluator (plugins/evaluator/api).
// Splitting these concerns lets mem0 / Graphiti / Letta authors
// implement only Store/Query/Forget without inheriting bough-specific
// trace-handling logic.
//
// The SQLite reference-fallback plugin ships in v0.5 as the minimum
// protocol-verification implementation and as the offline fallback
// when an external backend is unconfigured or unreachable. It is not
// a competitor to mem0 / Graphiti.
package api

import "context"

// MemoryBackend is the Go-side surface the bough host calls and that
// memory-plugin authors implement. Every error is wrapped at the gRPC
// boundary into a string in the wire response; the Go interface
// rematerialises plain errors so the host can `errors.Is` / wrap as
// usual.
//
// Lifecycle in the typical instinct flow:
//
//	Health        — pluginhost spawns the plugin, probes for liveness
//	Capabilities  — host learns which optional features are supported
//	Store         — coordinator upserts each approved instinct
//	Query         — `bough instinct query` and the retrieve pipeline
//	                hit Query; backend honours MaxResults/MaxTokens
//	                and reports EstimatedTokens + Truncated per result
//	Forget        — soft delete (state="forgotten"); decay is the
//	                coordinator's responsibility, not the backend's
//	Export/Import — `bough instinct export` / `import` round-trip
type MemoryBackend interface {
	Health(ctx context.Context, req *HealthReq) (*HealthResp, error)
	// Capabilities lets v0.6+ plugins advertise optional features
	// (semantic_query, graph_query, vector_search, bulk_export,
	// supports_metadata). v0.5 plugins should return all-false.
	Capabilities(ctx context.Context) (*CapabilitiesResp, error)
	// Store is an upsert. Backends MUST dedupe on (DedupeKey,
	// SourceEventID) so observer retries and JSONL re-reads are
	// idempotent.
	Store(ctx context.Context, req *StoreReq) (*StoreResp, error)
	// Query honours both MaxResults and MaxTokens; the backend
	// reports EstimatedTokens for each result so the host's budget
	// aggregator can stop iterating once the running total exceeds
	// the configured retrieve.max_tokens.
	Query(ctx context.Context, req *QueryReq) (*QueryResp, error)
	// Forget is a soft delete. Hard deletion is the coordinator's
	// decay scheduler's responsibility.
	Forget(ctx context.Context, req *ForgetReq) (*ForgetResp, error)
	Export(ctx context.Context, req *ExportReq) (*ExportResp, error)
	Import(ctx context.Context, req *ImportReq) (*ImportResp, error)
}
