# bough-plugin-memory-mem0-skeleton

Skeleton for a v0.6 mem0 adapter. v0.5 ships only this skeleton (no binary); the official `bough-plugin-memory-mem0` lands in v0.6.0.

## Why a skeleton in v0.5

The round 2.5 / round 3 external review made the cadence explicit:

- v0.5 freezes the wire contract so plugin authors can prototype.
- v0.6 ships the official mem0 adapter alongside Graphiti / Claude Skills export / MCP export.

The skeleton lets a community author start working against the v0.5 contract today, with the understanding that the canonical mem0 plugin will arrive in v0.6.

## What this skeleton shows

The structure of a mem0-backed `MemoryBackend`:

```go
type mem0Backend struct {
    client *mem0.Client      // hypothetical mem0 SDK client
    cfg    *mem0Config        // endpoint, api key, namespace template
}
```

- `Store(req)` POSTs to mem0's add-memory endpoint with `req.Instinct.Rule` as the body and a namespaced `user_id` derived from `req.Instinct.Scope`.
- `Query(req)` GETs / searches mem0's search-memory endpoint with `req.Term`, applies the host's `MaxResults` / `MaxTokens` caps, and computes `EstimatedTokens` per result.
- `Forget(req)` DELETEs from mem0's delete-memory endpoint.
- `Health` checks the mem0 endpoint's `/health`.
- `Capabilities` declares mem0's actual feature set (semantic query: true, vector search: true).
- `Export` / `Import` walk mem0's bulk endpoints.

## Namespace mapping

See `docs/NAMESPACE_MAPPING.md` for the canonical mapping. The mem0 plugin uses:

```
schema.Scope{Level: "worktree", WorktreeID: "F-x", RepoName: "auba"}
  → mem0 user_id = repo/<sha-of-remote>/worktree/F-x/<sha-of-root>
```

## When to finish this skeleton

When v0.6 ships you can either install the official `bough-plugin-memory-mem0` or keep your custom build. The official adapter aims for full feature parity with mem0's API; a custom build is useful when you want to layer on top of mem0 with extra namespacing or auth.
