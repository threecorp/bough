# MemoryBackend plugin contract (v0.5)

The MemoryBackend plugin contract is the canonical wire and Go
contract between the bough host and any `bough-plugin-memory-<name>`
binary that persists, queries, and audits per-worktree instinct
state.

This document supplements the in-source Go docs (`interface.go`,
`types.go`) and the proto definition (`proto/memory.proto`). Read
this when designing a v0.6+ adapter for mem0 / Graphiti / Letta or
any other external memory OSS.

## Bough is not a memory system

bough is a per-worktree memory orchestration layer. The
MemoryBackend interface is intentionally limited to persistence
concerns (`Health`, `Capabilities`, `Store`, `Query`, `Forget`,
`Export`, `Import`). Bough does NOT ask backends to:

- mint candidates from raw traces — that is `InstinctMinter`
  (plugins/instinct/api).
- compile instincts into reusable skills/commands/tools/agents —
  that is `CapabilityCompiler` (plugins/capability/api, v0.6+).
- score compiled artifacts — that is `SkillEvaluator`
  (plugins/evaluator/api, v0.7+).

Splitting these concerns lets mem0 / Graphiti / Letta authors
implement only persistence without inheriting bough-specific
trace-handling logic.

The SQLite reference-fallback plugin ships in v0.5 as the minimum
protocol-verification implementation and as the offline fallback
when an external backend is unconfigured or unreachable. It is not
a competitor to mem0 / Graphiti.

## Handshake

```
ProtocolVersion: 1
MagicCookieKey:  BOUGH_MEMORY_PLUGIN
MagicCookieValue: v1
PluginKey: memory_backend
```

The cookie is distinct from `BOUGH_ENGINE_PLUGIN` so a binary that
registers multiple plugin kinds (rare; the conformance mock does
this) is unambiguous about which contract is being dispensed.

## The seven RPCs

| RPC | When the host calls it | What the backend MUST do |
|---|---|---|
| `Health` | pluginhost discovery, conformance lifecycle | Reply in under ~50ms with backend_kind + plugin_version. |
| `Capabilities` | host learns which optional features the backend supports | v0.5 backends should return all optional features as `false`. v0.6+ backends may advertise semantic_query, graph_query, vector_search, bulk_export, supports_metadata. |
| `Store` | coordinator persists an approved instinct | Upsert: if an item with the same `id` or matching `dedupe_key` within the same scope already exists, increment `hits`, update `confidence` per the source policy, refresh `last_hit_at`. Both `dedupe_key` and `source_event_id` are required dedupe inputs so observer retries / JSONL re-reads / CI reruns are idempotent. |
| `Query` | `bough instinct query`, coordinator retrieve pipeline | Honour both `max_results` (count cap) and `max_tokens` (estimated-token cap). Return `estimated_tokens` and `truncated` per result so the host's budget aggregator can stop iterating. |
| `Forget` | `bough instinct forget <id>` | Soft delete: mark the row's `state` as `"forgotten"` but keep it for audit. Hard deletion is the coordinator's decay scheduler's responsibility, not the backend's. |
| `Export` | `bough instinct export` | Render the matching rows in the requested format. v0.5 supports `yaml` and `jsonl`; v0.6+ adds `claude-skills`, `agent-skill`, `mcp`. |
| `Import` | `bough instinct import <file>` | Treat as a batch of upserts (same dedupe semantics as `Store`). |

## State machine

```
candidate → active   (after `bough instinct approve` or hybrid mint auto-promotion)
candidate → forgotten (auto-forget at candidate_ttl_days)
active    → archived (decay scheduler hits a low-confidence row)
active    → forgotten (explicit `bough instinct forget`)
archived  → forgotten (eventually)
```

Forget is soft. Hard deletion is never performed by the backend.

## Dedupe key

```
dedupe_key = sha256(normalize(rule + scope.level + scope.worktree_id + scope.repo_name))
```

The host may pre-compute and pass `dedupe_key` in `StoreRequest`, or
the backend may compute it. In either case, two `Store` calls with
the same effective `dedupe_key` within the same scope MUST be
treated as a single upsert (not two rows).

`source_event_id` is an idempotency token observers populate (e.g.,
`session-id:row-N` for the JSONL tail, `ci-run-N` for CI ingest).
Two `Store` calls with the same `source_event_id` MUST be treated
as a no-op repeat.

## Budget semantics (round 3 AI #1)

Memory accumulation across runs WILL otherwise blow Claude's
context window. The host enforces hard limits via
`max_results` and `max_tokens` in `QueryRequest`. Backends MUST:

1. Honour both caps. Returning more results than `max_results`
   asked for is a contract violation.
2. Set `estimated_tokens` on each `QueryResult` so the host's
   budget aggregator can stop iterating once the running total
   exceeds `max_tokens`.
3. Set `truncated=true` on each result whose content was elided to
   meet the cap, so the host's CLI can surface "1 of 12 results
   truncated at 4000 tokens".

## Concurrency (round 3 AI #3)

`Store` (writes) and `Query` (reads) MUST be safe to run in
parallel. The host's coordinator may interleave a stdin-ingest
`Store` with a `bough memory query` triggered concurrently by the
same Claude session. Backends that wrap SQLite (= the reference-
fallback) MUST use WAL mode + `busy_timeout` to avoid "database is
locked" errors.

The conformance suite (`conformance/memory/concurrency.go`) drives
parallel Store/Query and asserts no errors and no lost writes.

## Metadata escape hatch (round 3 AI #2)

`Instinct.metadata_json` is a free-form JSON byte slot. v0.5
backends should accept and round-trip it through `Store` → `Query`
→ `Export` → `Import` without parsing. The SQLite reference-
fallback schema reserves a `metadata TEXT` column for exactly this.
v0.6+ backends (mem0 / Graphiti) MAY parse Metadata and project
internal fields from it (e.g., agent_id for mem0's multi-tenancy).

## Author checklist

A community / vendor memory plugin author should:

1. Implement `MemoryBackend` (7 methods) in Go.
2. Cross-build the binary as `bough-plugin-memory-<name>` for the
   target OS/arch matrix.
3. Drop a `conformance_test.go` next to the implementation:

   ```go
   //go:build conformance

   package <yourplugin>

   import (
       "testing"

       memconf "github.com/ikeikeikeike/bough/conformance/memory"
   )

   func TestConformance(t *testing.T) {
       memconf.Run(t, memconf.Config{
           Plugin:  "<name>",
           Datadir: t.TempDir(),
       })
   }
   ```

4. Wire CI to run `BOUGH_CONFORMANCE_MEMORY_PLUGIN_BIN=<path>
   go test -tags=conformance ./...`. The conformance suite covers
   lifecycle, invariants, concurrency, bloat, zombie process, and
   audit-failure cases.

5. Publish the binary alongside a `docs/INTEGRATION.md` in your
   plugin repo that explains namespace mapping, fallback policy,
   and the v0.5-stable feature subset your backend implements.
