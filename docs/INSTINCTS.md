# bough v0.5 instinct subsystem

> **bough is not an agent memory system. bough is a per-worktree memory orchestration layer.**

The v0.5 instinct subsystem lets bough host behavioural rules, observations, and reusable knowledge per worktree, repo, and global scope — without bough itself trying to be a memory engine. Instinct *intelligence* (storage, retrieval, embeddings, semantic search) is delegated to memory plugins; bough only provides the canonical schemas, the lifecycle, the safety pipeline (redaction, poisoning guard, dedupe, decay), and the conformance contract every backend honours.

## When to enable

Set `instinct.enabled: true` in `.bough.yaml`. The subsystem is **off by default** in v0.5 so existing v0.4 monorepos see no behavioural change after upgrade.

```yaml
schema_version: 2
# ... existing v0.4 sections ...

instinct:
  enabled: true
  default_memory_backend: sqlite
  default_instinct_minter: builtin

memory_backends:
  - kind: sqlite
    role: reference-fallback
    path: ".bough/memory/reference.db"
    fts: true
    wal: true
```

## The lifecycle

```
                    ┌────────────────────────┐
                    │ stdin ingest (PRIMARY) │ ← make test | bough instinct ingest --stdin
                    │ .jsonl tail  (beta)    │
                    └───────────┬────────────┘
                                │ TraceBundle
                  ┌─────────────▼─────────────┐
                  │ Redaction (PII / secrets) │
                  └─────────────┬─────────────┘
                                │
                  ┌─────────────▼─────────────┐
                  │ InstinctMinter (builtin)  │ → InstinctCandidate[]
                  └─────────────┬─────────────┘
                                │
                  ┌─────────────▼─────────────┐
                  │ ConfidencePolicy clamp    │
                  │   explicit_user 0.75      │
                  │   test_failure  0.60      │
                  │   session_log   0.45      │
                  │   llm_only      0.30      │
                  └─────────────┬─────────────┘
                                │
                  ┌─────────────▼─────────────┐
                  │ PoisoningGuard            │
                  │   - dedupe sha256         │
                  │   - approval gate         │
                  │   - max_active_per_scope  │
                  └─────────────┬─────────────┘
                                │
                  ┌─────────────▼─────────────┐
                  │ MemoryBackend.Store       │ ← gRPC plugin (sqlite / mem0 / graphiti)
                  └───────────────────────────┘
```

## CLI

```sh
# Ingest CI / test output as the primary path.
make test 2>&1 | bough instinct ingest --stdin --source test_failure --source-event-id $(git rev-parse HEAD)

# Manual mint from an explicit rule.
bough instinct mint --rule "prefer early returns over nested if" --source explicit_user_feedback

# Approve a candidate so it goes active.
bough instinct approve <id>

# Search.
bough instinct query --term "early returns"

# Move from worktree → repo or repo → global.
bough instinct promote <id> --to repo

# Soft delete.
bough instinct forget <id> --reason "rule turned out wrong"
```

## Scope tiers

- **worktree**: lifetime = the worktree. Tied to a specific branch / feature.
- **repo**: lifetime = the monorepo. Survives worktree churn.
- **global**: lifetime = follows the user across monorepos.

Promote with `bough instinct promote <id> --to repo|global`. The host issues a `Store(scope=target)` + `Forget(scope=source)` pair so any backend implements the move identically (round 3 AI #1 design: bough owns scope, never the backend).

## Safety

- **Redaction** — `instinct.mint.redaction.enabled: true` strips email / api_key / token / password / aws_secret patterns from raw content before any minter sees it.
- **Confidence ceiling** — `instinct.confidence.sources.<source>` caps the initial confidence a candidate from a given source can stamp. LLM-only is 0.30; explicit user feedback is 0.75.
- **Poisoning guard** — `instinct.mint.mode: hybrid` (the default) puts every new candidate in `state: candidate` until `bough instinct approve` flips it active.
- **Max active per scope** — `instinct.poisoning_guard.max_active_per_scope: 200` keeps any single scope from accumulating unbounded rules.
- **Candidate TTL** — un-approved candidates auto-forget after `candidate_ttl_days: 14`.
- **Decay** — active rules past `decay_after_days: 30` move to `archived`.

## Observer

The v0.5 PRIMARY ingest path is `bough instinct ingest --stdin`. The `.jsonl` file watch observer is opt-in beta because of fsnotify's cross-platform fragility (macOS FSEvents vs Linux inotify divergence, log rotation, truncate). Enable it explicitly:

```yaml
instinct:
  observer:
    file_watch:
      enabled: true
      stability: beta
      jsonl_path_template: "~/.claude/projects/<id>/<session>.jsonl"
      rotation_handling: true
      truncate_handling: true
      debounce_ms: 100
```

## See also

- [BACKENDS.md](BACKENDS.md) — choosing a memory backend.
- [EXTERNAL_MEMORY_BACKENDS.md](EXTERNAL_MEMORY_BACKENDS.md) — wiring mem0 / Graphiti adapters.
- [SECURITY.md](SECURITY.md) — third-party plugin trust + SKILL.md supply-chain risk.
- [ROADMAP.md](ROADMAP.md) — v0.5 → v0.6 → v0.7+ plan.
