# Memory plugin author guide

Step-by-step for writing a `bough-plugin-memory-<name>` binary that satisfies the v0.5 contract.

## Prerequisites

- Go 1.22+ (the bundled SQLite reference uses generics and `slices` package)
- `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` (only if you regenerate the protocol)
- `make` and the bough source tree for conformance testing

## Steps

1. **Read the contract**

   - `plugins/memory/api/CONTRACT.md` — the wire contract, RPCs, state machine, dedupe / source_event_id rules, budget semantics, concurrency / metadata invariants.
   - `plugins/memory/api/interface.go` — the Go-side surface.
   - `plugins/memory/sqlite/sqlite.go` — the bundled reference. Read for the minimal-correct shape.

2. **Copy the template**

   `examples/memory-plugin-template/` is a working minimal plugin. Copy it as `bough-plugin-memory-<yourname>/` in your own repo:

   ```sh
   cp -r bough/examples/memory-plugin-template/ my-plugin/
   cd my-plugin/
   ```

3. **Implement `MemoryBackend`**

   The 7 methods: `Health`, `Capabilities`, `Store`, `Query`, `Forget`, `Export`, `Import`. The template wires the gRPC plumbing; replace the body of each method with your backend's call.

   v0.5 plugins should declare `Capabilities.SemanticQuery / GraphQuery / VectorSearch / BulkExport` as `false`. The host trusts the value: if you say true, the host will route relevant queries to you.

4. **Honour the dedupe contract**

   - `Store` MUST handle `dedupe_key` and `source_event_id` as documented. Round 3 AI #1 made these idempotency tokens required.
   - Returning `WasUpsert=false` on what is really an upsert is a contract violation the conformance suite catches.

5. **Honour the budget contract**

   - `Query` MUST respect `MaxResults` and `MaxTokens`.
   - Set `EstimatedTokens > 0` on each result with non-empty content so the host's budget aggregator can stop iterating.
   - Set `Truncated = true` on any result whose content was elided to meet the cap.

6. **Run conformance**

   ```sh
   go build -o dist/bough-plugin-memory-<name> ./cmd/bough-plugin-memory-<name>
   BOUGH_CONFORMANCE_MEMORY_PLUGIN_BIN=$PWD/dist/bough-plugin-memory-<name> \
       go test -tags=conformance ./...
   ```

   Three sub-tests run: `Lifecycle` (the 7-RPC walk), `Bloat` (cap honouring), and `Concurrency` (parallel Store + Query). All three must pass.

7. **Document namespace mapping**

   v0.6 external backends translate `schema.Scope` into their own namespaces. Document your mapping in your repo's `docs/INTEGRATION.md` — see `docs/NAMESPACE_MAPPING.md` for the canonical bough convention.

8. **Document fallback policy**

   If your backend can fail intermittently (network calls, remote service timeouts), document how `instinct.fallback_on_error: true` is meant to interact with it.

9. **Ship**

   Publish the binary alongside a README that links to your `docs/INTEGRATION.md`. Users install by adding your binary to PATH and adding a `memory_backends: [{kind: <name>, role: external, ...}]` block to `.bough.yaml`.

## Common pitfalls

- **Storing more than `MaxResults` rows** — the host trims, but the conformance suite still flags the over-return as a contract violation.
- **Returning `EstimatedTokens=0` for non-empty rules** — the budget aggregator can't stop iterating, so subsequent `bough instinct query` calls hit the cap-on-cap problem.
- **Not setting WAL / busy_timeout on SQLite-wrapping backends** — the concurrency conformance test will fail with "database is locked".
- **Honouring scope filter only on Query but not on Forget** — a `bough instinct forget` against scope=worktree should not delete the repo-scoped row with the same ID.

## See also

- `examples/memory-plugin-template/` — minimal working plugin.
- `examples/memory-plugin-mem0-skeleton/` — the v0.6 mem0 adapter pattern.
- `plugins/memory/api/CONTRACT.md` — the wire contract.
- `conformance/memory/` — the test suite you must pass.
