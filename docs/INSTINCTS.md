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

## Audit log (events.jsonl)

Every coordinator action (mint, approve, store, query, forget, promote, decay, import) appends one line to `events.jsonl`. The file is bough's source of truth when an external backend disagrees with the local mirror — replay it to reconstruct what *should* have happened.

The path is **always absolute**: bough resolves a relative `memory_backends[].events_log` against the `loadConfigAndRoot` monorepo root, and `NewEventWriter` rejects relative paths up front (round 3 LOW #18). This is what stops two worktrees, or a CI step + a dev shell, from racing on different cwd-relative files.

Default location: `<monorepo-root>/.bough/memory/events.jsonl`. Override via the backend block:

```yaml
memory_backends:
  - kind: sqlite
    role: reference-fallback
    path: ".bough/memory/reference.db"      # relative → resolved against monorepo root
    events_log: ".bough/memory/events.jsonl" # same — or supply an absolute path
```

## Related projects

bough's instinct subsystem sits in **Layer C (artifact compile chain)**;
the projects below cover Layer A (memory architecture) and Layer B
(skill execution orchestration) and either complement bough or solve
a different layer entirely. See [CONCEPTS.md](CONCEPTS.md) for the
three-layer split.

| Project | Layer | Relation to bough |
|---|---|---|
| [affaan-m/everything-claude-code (ECC)](https://github.com/affaan-m/everything-claude-code) + [affaan-m/ECC](https://github.com/affaan-m/ECC) | C (and a touch of B via hooks) | Same Layer C answer (parallel compile target via `skill / command / agent` slash branching), but ECC fires from Claude Code's `PreToolUse` / `PostToolUse` / `SessionEnd` / `PreCompact` hooks. bough's `instinct ingest` is the explicit-CLI counterpart. v0.7 Bootstrap layer is where the two trigger models meet. |
| [Letta Context Repositories](https://www.letta.com/blog/context-repositories/) + [Letta memfs](https://docs.letta.com/letta-code/memfs) | A | Git-backed memory filesystem with worktree-isolated subagents and sleep-time defragmentation. A `MemoryBackend` plugin candidate for v0.7. |
| [mem0](https://mem0.ai/) | A | Shipped as a first-class plugin in v0.6 (`bough-plugin-memory-mem0`). |
| [Graphiti (getzep)](https://help.getzep.com/graphiti/) | A | Skeleton + docker-compose snippet ship in v0.6; binary plugin lands in v0.6.x. |
| [Anthropic Claude Skills](https://www.anthropic.com/engineering/equipping-agents-for-the-real-world-with-agent-skills) + [code.claude.com/docs/en/skills](https://code.claude.com/docs/en/skills) | B (the runtime that reads `claude-skill` emitter output) | bough emits SKILL.md via `bough capability compile --to claude-skill`; the Skills runtime decides when to load it. |
| [Model Context Protocol (spec 2025-11-25)](https://modelcontextprotocol.io/specification/2025-11-25) | B (transport) | bough emits MCP tool / resource / prompt bundles via `--to mcp`; `bough-mcp-server` speaks the protocol. |
| [SkillX (zjunlp)](https://github.com/zjunlp/SkillX) / [SkillRT](https://arxiv.org/html/2604.03088v1) / [GraSP](https://arxiv.org/html/2604.17870v1) / [AgentSkillOS](https://arxiv.org/html/2603.02176v1) | B (skill execution critique) | 2026 anti-pattern papers on flat / sequential skill libraries. Their critique targets Layer B, not bough's Layer C compile dispatch — but the alternative shapes they propose (typed DAG, direct-sum tier, parallel compile target) inform bough's `CapabilityArtifactKind` set. |
| [AtomMem](https://arxiv.org/abs/2601.08323) | A (memory CRUD critique) | Argues that static hand-crafted memory workflows are anti-pattern. Useful background for plugin authors choosing between mem0 / Letta / Graphiti / SQLite. |

## See also

- [CONCEPTS.md](CONCEPTS.md) — the three-layer model (memory architecture / skill execution / artifact compile chain).
- [BACKENDS.md](BACKENDS.md) — choosing a memory backend.
- [EXTERNAL_MEMORY_BACKENDS.md](EXTERNAL_MEMORY_BACKENDS.md) — wiring mem0 / Graphiti adapters.
- [SECURITY.md](SECURITY.md) — third-party plugin trust + SKILL.md supply-chain risk.
- [ROADMAP.md](ROADMAP.md) — v0.5 → v0.6 → v0.7 plan.
