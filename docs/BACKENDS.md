# bough memory backends

The v0.5 instinct subsystem persists instincts through a pluggable `MemoryBackend` gRPC contract. The bough host discovers a `bough-plugin-memory-<kind>` binary, spawns it under hashicorp/go-plugin, and routes Store / Query / Forget / Export / Import / Capabilities / Health calls through it.

## Responsibility split

| layer                       | bough owns                             | backend owns |
|-----------------------------|----------------------------------------|--------------|
| scope orchestration         | yes                                    | no           |
| redaction policy            | yes                                    | optional     |
| storage algorithm           | no (except the SQLite reference)       | yes          |
| semantic memory quality     | no                                     | yes          |
| lifecycle / export contract | yes                                    | yes          |

bough is the orchestration layer. The backend implements storage and retrieval. The bough vision (round 1 + round 2 + round 3 external review) deliberately separates these so we can plug mem0, Graphiti, Letta — or your own — into the same contract.

## Available backends (v0.5)

| backend  | role               | when to use                                            | binary                            |
|----------|--------------------|--------------------------------------------------------|-----------------------------------|
| sqlite   | reference-fallback | offline development, conformance, GitHub Release single-binary install, audit fallback for an external backend | `bough-plugin-memory-sqlite` (bundled) |

The SQLite backend is **the reference-fallback**, NOT a competitor to mem0 / Graphiti. It is the minimum-correct implementation of the v0.5 wire contract: every v0.6+ adapter (mem0 / Graphiti / Letta / community) reads `plugins/memory/sqlite/` as the shape it must mirror. See `plugins/memory/api/CONTRACT.md`.

## Choosing a backend

| use case                                       | recommended           |
|------------------------------------------------|-----------------------|
| solo dev, no team data sharing                 | `sqlite` (v0.5)       |
| team-scale memory across worktrees             | `mem0` (v0.6+)        |
| temporal facts that change over time           | `graphiti` (v0.6+)    |
| OS-level agent runtime with own memory         | (use Letta directly)  |

## Configuration

```yaml
instinct:
  enabled: true
  default_memory_backend: sqlite     # name must match a memory_backends[].kind below
  fallback_on_error: true             # external backend timeout → SQLite reference-fallback

memory_backends:
  - kind: sqlite
    role: reference-fallback
    path: ".bough/memory/reference.db"
    fts: true                         # FTS5 virtual table for term search
    wal: true                         # WAL mode (round 3 AI #3 concurrency contract)
    busy_timeout_ms: 5000
```

## v0.6+ external backends

Adapter skeletons land in `examples/memory-plugin-mem0-skeleton/` so plugin authors can prototype against the v0.5 contract today. The actual official mem0 / Graphiti plugins ship in v0.6.0 — see [ROADMAP.md](ROADMAP.md).

## Authoring a new backend

See `plugins/memory/api/CONTRACT.md` for the wire contract and `MEMORY_PLUGIN_AUTHOR_GUIDE.md` for the build / conformance / publish flow.
