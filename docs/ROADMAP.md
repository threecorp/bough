# bough roadmap

Round 3 external review (June 2026) settled the v0.5 → v0.6 → v0.7+ shape. This document is the canonical reference; the release CHANGELOG ties specific commits back to each item.

## v0.5.0 — Foundation

- ✅ 4 plugin contracts frozen (`plugins/{memory,instinct,capability,evaluator}/api/`)
- ✅ Canonical schemas in `pkg/schema/` (TraceBundle, InstinctCandidate, Instinct, CapabilityArtifact)
- ✅ `MemoryBackend` interface with 7 methods (Health, Capabilities, Store, Query, Forget, Export, Import)
- ✅ `InstinctMinter` interface with 1 method; bough core ships a built-in default
- ✅ `CapabilityCompiler` and `SkillEvaluator` interfaces frozen as stubs for v0.6 / v0.7+
- ✅ SQLite reference-fallback plugin with WAL + busy_timeout + FTS5 + metadata column
- ✅ Coordinator with redaction, poisoning guard, source-aware confidence, decay scheduler, promote, events.jsonl audit
- ✅ Stdin ingest as the PRIMARY observer path
- ✅ Claude `.jsonl` file watch as opt-in beta with inode rotation + truncate handling
- ✅ `bough instinct` and `bough memory` CLI subcommands
- ✅ conformance/memory + conformance/instinct suites with mock plugins
- ✅ pluginhost legacy v0.3 fallback removed

## v0.5.1 — Round 3 follow-up patch (shipped 2026-06-20)

Drop-in patch on top of v0.5.0; no schema, plugin contract, or binary-API changes.

- ✅ `instinct.fallback_on_error` consumed by `coordinator.Query` (MEDIUM #15)
- ✅ `bough memory import` / `bough instinct import` actually restore rows (MEDIUM #17)
- ✅ `events.jsonl` path required to be absolute, anchored to the monorepo root (LOW #18)
- ✅ Round-trip regression tests for SQLite YAML/JSONL Import

## v0.6.0 — External memory + capability compilation

Round 4 external review (June 2026) scoped v0.6.0 to mem0 first-
class + capability compile + read-only MCP + signing scaffolding;
Graphiti is deferred to v0.6.x as a separate GoReleaser archive.

- ✅ mem0 official plugin (`bough-plugin-memory-mem0`) with
  namespace mapping + 30s TTL Query cache + Read-only fallback to
  the SQLite reference-fallback (round 4 AI #1 + #2 split-brain
  Blocker 1 mitigation)
- ✅ MemoryBackend.Capabilities advertise widened to 17 fields
  (semantic / vector / graph / temporal / namespace / metadata /
  soft_delete / ttl / dedupe_key / source_event_id / bulk_import /
  bulk_export / eventual_consistency / max_batch_size /
  max_query_tokens, round 4 priority A12)
- ✅ `CapabilityCompiler` materialised with deterministic Checksum
  + Target / Invocation / Contract / Validation / Provenance
  metadata groups (round 4 priority A3)
- ✅ `bough capability compile --to <agent-skill|claude-skill|mcp>
  --profile <host>` — agent-skill is the v0.6 default (round 4
  priority A2: bough is a host-neutral OSS layer)
- ✅ Three builtin emitters (`agent-skill`, `claude-skill`,
  `mcp`) with the Emitter interface lifted into
  `plugins/capability/api/` so v0.6.x can graduate them into
  plugin slots (round 4 priority A13)
- ✅ `bough-mcp-server` companion binary with read-only first
  surface, MCP spec_version pin 2025-11-25, and the round 4 AI #1
  stdin-EOF zombie guard
- ✅ Plugin signing scaffolding: cosign + minisign acceptance,
  `bough plugins verify`, GoReleaser keyless integration
  (full enforcement timeline in docs/SIGNING.md)
- ✅ Graphiti skeleton + docker-compose snippet (binary in v0.6.x)
- ✅ docs/CAPABILITY_COMPILER.md, docs/MCP_SERVER.md,
  docs/SIGNING.md ship alongside the binaries

## v0.6.x — Patch + experimental compilers

Dogfooding the v0.6.0 ship surfaced one false-negative in the host
config validator and re-framed the v0.7 Bootstrap design. v0.6.x
absorbs both before the next minor.

- `bough config validate` accepts v0.5+ root sections (`instinct`,
  `engines`, `memory_backends`, `export`) — the v0.5 schema bump
  forgot to mirror the new fields into the LegacyConfig superset the
  validator's first-pass decoder uses, so `validate` reports
  `unknown field` while every other subcommand loads the file
  cleanly. (Post-ship finding, 2026-06-22.)
- `bough memory reconcile --from sqlite --to mem0` materialises the
  split-brain recovery story the v0.6 mem0 plugin promised in
  `docs/EXTERNAL_MEMORY_BACKENDS.md`.
- `bough-mcp-server --allow-write` and the three state-changing
  Tools (`memory.store`, `memory.forget`, `memory.promote`), all
  routed through the same audit log the v0.5 coordinator writes.
- Plugin signing strict mode (`require_signed: true` actually
  refuses to spawn unverified plugins; v0.6.0 only exposes the
  verify CLI).
- SkillX adapter (round 3 AI #3: zjunlp/SkillX research repo)
- Anything2Skill-style compiler
- Alita-G MCP tool compiler
- Experimental compilers ship as community / experimental plugins
  under `examples/`.

## v0.7 — Bootstrap layer

The v0.6 retrospective (2026-06-22) clarified that the user-facing
intent — "`claude --worktree X` materialises an isolated dev
environment **and** generates the artifacts the next session will
need" — needs a dedicated layer above the existing CapabilityCompiler.
The Layer C compile path is correct as-is; what's missing is a
trigger model that fires *on first worktree open* and a Bootstrap
Agent specification that picks which of the seven artifact kinds to
materialise from the materialised sub-repos.

Concretely:

- `bough init` CLI (manual trigger; the same code path the hook fires)
- `WorktreeCreate`-time invocation of a configured Bootstrap Agent
  (opt-in; off by default per existing safety posture)
- Bootstrap Agent specification: input sources (repo tree, git log,
  `CLAUDE.md`, `tasks/lessons.md`, prior `.bough/memory/`), output
  contract (artifact kind × candidate state), guardrails (rate limit,
  artifact cap per scope, hard token budget)
- Artifact persistence layer formalised (where each kind lands; how
  the lifecycle interacts with `bough instinct promote` / `forget`)
- ECC interop: when ECC's hook pipeline is already installed in a
  monorepo, bough's Bootstrap Agent stays out of its way (config flag
  `bootstrap.deferred_to_ecc: true`).
- Letta Context Repositories interop: optionally treat the bough
  memory filesystem as a Letta `memfs`-compatible projection so
  Letta-native tools can read bough state without translation.
- `SkillEvaluator` first materialisation so generated artifacts get
  measured against the trajectories that produced them.
- GEPA / TextGrad / MUSE-Autoskill / SkillAudit adapters as
  plug-in evaluator backends.

## What bough deliberately does NOT do

- weight updates (SEAL / SFT / RLHF) — model-tier concern, not orchestration
- `instinct → skill → command → agent` as a forced single chain — round 1 rejected the **chain** in favour of a parallel compile target set. This rejection is a Layer C decision and is **orthogonal** to the 2026 Layer A (memory CRUD) and Layer B (skill execution) anti-pattern literature; see `docs/CONCEPTS.md` for the three-layer split. The seven parallel targets (memory, rule, skill, command, tool, agent, evaluator) all remain valid sinks; the InstinctCandidate metadata picks which subset to materialise.
- proprietary vendor memory (OpenAI Memory, Anthropic Memory) — vendor lock-in
- GPL/AGPL backends — license drift for downstream MIT/Apache users

## Why these choices

The full design history lives in the round 1 / 2 / 2.5 / 3 synthesis notes (see PR #N). The recurring thread: **bough is a per-worktree memory orchestration layer, not an agent memory system.** Every choice on this roadmap reinforces that boundary.
