# Namespace mapping for v0.6+ external memory backends

## Canonical mapping

When a v0.6+ memory plugin (mem0 / Graphiti / Letta) needs to translate a bough scope into its own user_id / session_id / agent_id namespace, the host's canonical mapping is:

```
global   →  global/<user_or_machine_id>
repo     →  repo/<repo_remote_hash>/<monorepo_root_hash>
worktree →  repo/<repo_remote_hash>/worktree/<branch>/<worktree_root_hash>
```

`<user_or_machine_id>` defaults to the OS user@hostname. `<repo_remote_hash>` is `sha256(git remote get-url origin)` truncated to 16 hex chars; `<monorepo_root_hash>` is `sha256(absolute path)` truncated similarly.

## Why not branch name alone

Two monorepos that both have a `main` branch, or that both happen to use a `F-some-feature` convention, would collide if the mapping were branch-name-only. The repo-remote-hash prefix keeps namespaces isolated even when branch names overlap.

## When the mapping matters

- The host's coordinator never sees this mapping — it works in `schema.Scope` and lets the backend translate at the wire.
- v0.6 mem0 / Graphiti plugin authors implement the mapping inside their `MemoryBackend.Store` / `Query` handlers.
- The conformance suite passes `schema.Scope{Level: "worktree", WorktreeID: "F-test", RepoName: "auba"}`; backends are free to translate however they like internally as long as round-trip Store → Query honours the scope filter.

## Implementation notes

```go
// Sketch for a mem0 plugin author.
func userIDForScope(s schema.Scope) string {
    switch s.Level {
    case "global":
        return "global/" + machineID()
    case "repo":
        return fmt.Sprintf("repo/%s/%s", hashRepo(), hashRoot())
    case "worktree":
        return fmt.Sprintf("repo/%s/worktree/%s/%s", hashRepo(), s.WorktreeID, hashRoot())
    }
    return ""
}
```

The host does not enforce the mapping — that is the plugin author's contract. The conformance suite verifies that storing under one scope and querying under a different scope return disjoint results, which is enough to catch namespace bleed.

See [EXTERNAL_MEMORY_BACKENDS.md](EXTERNAL_MEMORY_BACKENDS.md) for adapter author guidance.
