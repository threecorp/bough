# bough-plugin-memory-mem0 — CONTRACT

This document pins the contract between bough's v0.6 MemoryBackend
host and the mem0 (https://github.com/mem0ai/mem0) REST API the
plugin speaks. The contract is what makes the plugin swappable:
the host code references *interface behaviour*, not mem0's HTTP
shape.

## Scope translation

bough's three-tier Scope maps to mem0's namespaces as:

| bough Scope | mem0 namespace |
|---|---|
| `global` | `user_id = global/<machine-or-user-id>` |
| `repo` | `user_id = repo/<repo_remote_hash>/<root_hash>` |
| `worktree` | `user_id = repo/<repo_remote_hash>/<root_hash>` + `session_id = worktree/<worktree_id>` |

The repo / worktree split (= round 4 AI #2) puts the long-lived
identity on `user_id` and the short-lived per-branch identity on
`session_id`. This matches mem0's memory-types layering
(user / session / agent / run); the host owns the SHA computation
for `<repo_remote_hash>` and `<root_hash>` so multiple worktrees of
the same repo share the user namespace.

See `docs/NAMESPACE_MAPPING.md` (https://github.com/ikeikeikeike/bough/blob/main/docs/NAMESPACE_MAPPING.md)
for the canonical mapping rules.

## Fallback policy (= round 4 AI #1 + #2 Blocker 1)

- **Query (Read)**: if mem0 fails, the host coordinator replays the
  same `QueryReq` against the SQLite reference-fallback when
  `instinct.fallback_on_error: true`. This is intentional —
  read-side stale data is preferable to query loss.
- **Store (Write)**: if mem0 fails, the plugin returns an error and
  the host coordinator does NOT fall back to SQLite. Falling back
  to SQLite on writes causes split-brain (= mem0 holds N rows,
  SQLite holds N+1). v0.6.x will ship `bough memory reconcile
  --from sqlite --to mem0` for the asynchronous Sync Queue case.

`Forget`, `Export`, and `Import` follow the Store policy: they do
not fall back, they fail loudly so an operator can investigate.

## Cache (= round 4 AI #1 + #2)

The plugin keeps an in-process read-through cache to absorb
duplicate Query traffic across one bough invocation:

- TTL: 30 seconds (= short enough that mem0 updates from another
  bough process are visible within one query round)
- Key: `(Scope.Level, Scope.WorktreeID, Scope.RepoName, Term,
  MaxResults, MaxTokens, MinConfidence)`
- Eviction: LRU, max 512 entries
- Invalidation: any successful `Store`, `Forget`, or `Import` on a
  scope evicts every cached query result whose key targets that
  scope

The cache is **Query-only**. Store / Forget / Export / Import never
consult or populate it.

## Capabilities advertised (= round 4 priority A12)

The plugin advertises the v0.6 17-field `CapabilitiesResponse` as:

| Field | Value | Why |
|---|---|---|
| `semantic_query` | true | mem0's core strength: dense vector + reranker |
| `vector_search` | true | mem0 backs Query with ANN by default |
| `bulk_export` | true | mem0 supports paginated batch reads |
| `bulk_import` | true | mem0 supports paginated batch writes |
| `supports_metadata` | true | mem0 metadata is what holds bough's evidence refs |
| `metadata_filter` | true | mem0 filters on metadata server-side |
| `namespace_isolation` | true | `user_id` natively isolates Scope |
| `soft_delete` | true | Forget removes the row; queryability stops |
| `ttl` | true | mem0 supports per-memory automatic expiry |
| `eventual_consistency` | true | mem0 cloud may return Store success before durable |
| `dedupe_key` | false | mem0 has no native dedupe — host computes |
| `source_event_id` | false | same |
| `graph_query` | false | Graphiti's domain |
| `temporal_query` | false | Graphiti's domain |
| `max_batch_size` | 0 | unlimited (= mem0 paginates) |
| `max_query_tokens` | 0 | unlimited (= host's MaxTokens budget owns the cap) |

## API version

- mem0 REST API path prefix: `/api/v1/memories`
- API version pin: v1
- Breaking mem0 API changes are absorbed via a plugin patch release
  (= the plugin's `Capabilities()` advertise stays the same, only
  the wire mapping changes)

## Authentication

Required:

- `BOUGH_MEMORY_MEM0_ENDPOINT` — mem0 base URL.
  - Cloud: `https://api.mem0.ai`
  - Self-hosted: whatever the operator runs

Optional:

- `BOUGH_MEMORY_MEM0_API_KEY` — mem0 organisation API key. Omit
  for self-hosted instances that do not require auth.
- `BOUGH_MEMORY_MEM0_NAMESPACE` — optional multi-tenant prefix
  prepended to every `user_id`.
- `BOUGH_MEMORY_MEM0_TIMEOUT` — Go duration (`"10s"`); default 10s.

## What this plugin is NOT

- Not an embedded mem0 server (= bough does not bundle mem0 itself).
- Not a memory intelligence layer (= bough delegates ranking,
  embeddings, semantic retrieval to mem0).
- Not a write-side fallback (= split-brain prevention is the host
  coordinator's job; this plugin reports errors honestly so the
  host can refuse the write).
