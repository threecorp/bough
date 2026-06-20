# External memory backends (mem0 / Graphiti / Letta)

The `bough memory status` CLI notice that suggests "consider mem0 or graphiti" points here. This document explains how to wire an external memory backend once one ships (v0.6+) and what the adapter author needs to know.

## v0.5 status

| backend  | v0.5 status                                       | v0.6 plan                       |
|----------|---------------------------------------------------|---------------------------------|
| mem0     | adapter skeleton (`examples/memory-plugin-mem0-skeleton/`) | official `bough-plugin-memory-mem0` |
| Graphiti | not yet                                           | optional plugin, separate release artifact |
| Letta    | not yet                                           | community plugin (Letta is agent runtime, not memory layer) |

v0.5 ships the wire contract and the SQLite reference-fallback so the upgrade path to v0.6 is purely additive: bumping a config flag and installing the new plugin binary.

## Why we delegate

bough is intentionally not a memory engine. Round 1 → 2.5 → 3 of external AI design review converged on a clear rule:

- Memory store / retrieval is a solved problem with mature OSS (mem0 / Graphiti / Letta).
- Skill extraction / capability compilation is still a research-stage open question.
- bough therefore owns canonical schemas, scope, lifecycle, safety, and conformance — but never the memory intelligence itself.

The SQLite reference-fallback exists for offline development, conformance testing, GitHub Release single-binary install, and audit fallback when the configured external backend is unreachable. It is not competing with mem0 / Graphiti.

## v0.6 mem0 plugin author notes

The skeleton at `examples/memory-plugin-mem0-skeleton/` lays out the shape: HTTP client against mem0's REST API, `MemoryBackend` interface implementation, plugin.Serve wrapper, conformance test.

### Namespace mapping (canonical)

mem0's `user_id` / `agent_id` / `session_id` triple maps onto bough's scope as:

```
global   →  user_id = global/<user_or_machine_id>
repo     →  user_id = repo/<repo_remote_hash>/<monorepo_root_hash>
worktree →  user_id = repo/<repo_remote_hash>/worktree/<branch>/<worktree_root_hash>
```

The branch-name-only mapping would collide whenever two repos use the same branch name (`main`, `F-feature`). The canonical mapping uses the repo remote URL hash to namespace correctly.

### Fallback policy

bough's `instinct.fallback_on_error: true` flag tells the host to silently degrade to the SQLite reference-fallback when mem0 times out / errors. Set to `false` if you want loud failures during debugging.

## v0.6 Graphiti plugin

Graphiti (Zep) is a temporal knowledge graph backend. Best suited for cases where:

- facts change over time and you want temporal queries
- entities and relations matter more than raw text matches
- team-scale memory needs an audit / provenance graph

Graphiti needs a Postgres + a service process. It is intentionally heavier than mem0; the v0.6 plan ships it as a separate optional release artifact rather than bundling.

## Community plugins

Adapter authors should:

1. Read `plugins/memory/api/CONTRACT.md` for the wire spec.
2. Copy `examples/memory-plugin-template/` as a starting point.
3. Run the conformance suite against your binary:
   ```
   BOUGH_CONFORMANCE_MEMORY_PLUGIN_BIN=$PWD/dist/bough-plugin-memory-<name> \
       go test -tags=conformance ./...
   ```
4. Publish the binary alongside your own `docs/INTEGRATION.md` covering namespace mapping, auth, and the v0.5-stable feature subset your backend implements.

See [SECURITY.md](SECURITY.md) for the trust model around third-party plugins.
