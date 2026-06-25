# bough roadmap

Round 3 external review (June 2026) settled the v0.5 → v0.6 → v0.7+ shape. This document is the canonical reference; the release CHANGELOG ties specific commits back to each item.

## v0.9.2 — Full loop (shipped 2026-06-25)

Closes the continuous-learning loop. v0.9.0 observed, v0.9.1
evolved; v0.9.2 injects what was learned into the next session,
reinforces useful instincts at session end, and migrates existing
ECC corpora.

- ✅ `bough inject-context` — UserPromptSubmit hook, confidence-
  ranked instinct block (~9.5 KB cap), pure filesystem. Wired into
  `bough hook handle --event UserPromptSubmit` so one entry records
  + injects.
- ✅ `bough session-end` — SessionEnd hook, reinforces exercised
  instincts one confidence band up + appends eval/scores.jsonl.
- ✅ `bough preserve-instincts` — PreCompact hook, MEMORY.md top-5
  snapshot.
- ✅ `bough observer start/stop/status` — opt-in background daemon
  (PID-file lifecycle, Setsid detach, no systemd/launchd).
- ✅ `bough ecc import` — migrate an existing ECC corpus into
  bough's namespace (dry-run default; --apply copies).

The `claude --worktree X` → observe → evolve → inject loop is now
end-to-end. The v0.9 ECC port is complete.

## v0.9.1 — Evolve pipeline (shipped 2026-06-25)

The evolve half of the ECC port. v0.9.0 shipped the observer (=
instinct extraction); v0.9.1 ships the five-gate clustering pipeline
that turns instincts into skills / agents / commands.

- ✅ `bough evolve` (preview, no LLM) / `bough evolve --generate`
  (GATE 5 + emit). The ECC `/evolve-skill-manual-v3` UX.
- ✅ `internal/evolve/` — tokenize / Jaccard / coverage, connected-
  component clustering, the four mechanical gates (ECC v3 verbatim:
  MEMBER_MIN=2 / COH_MIN=0.20 / LEXICON_COVERAGE_MAX=0.55 /
  REL_ISOLATION_MIN=0.40), GATE 5 LLM judge via `claude --print`,
  cluster-labels.json with the sacred-string rule + backup, and the
  SKILL.md / agent / command emitters.
- ✅ GATE 5 verdict routing: PASS mints a fresh label, DOUBT reuses
  the nearest prior label, FAIL rejects.
- ✅ Agent eligibility (cluster >= 3 + avg conf >= 0.75) + command
  eligibility (workflow domain + conf >= 0.70), ECC thresholds.
- ✅ `claudecli.Provider.GenerateRaw` for pre-rendered prompts.

v0.9.2 (= upcoming): `bough inject-context` UserPromptSubmit hook +
SessionEnd handlers + PreCompact + optional observer daemon +
`bough ecc import`.

## v0.9.0 — ECC verbatim port (shipped 2026-06-23)

The "reset to the operator's vision" release. v0.5-v0.8 accreted
memory backends, capability compilers, MCP server, evaluator
adapters, judges, ECC import helpers — none of which the operator's
vision needs. v0.9 deletes them and ships threecorp ECC's
continuous-learning architecture verbatim in Go.

Mechanism: `claude --print` subprocess. No Anthropic API call. LLM
cost stays inside the operator's existing Claude Code subscription.

- ✅ `internal/homunculus/` — `~/.local/share/bough-homunculus/`
  layout, project_id (= sha256[:12] of git remote URL stripped),
  atomic registry, instinct file IO with filename ↔ id enforcement.
- ✅ `internal/observe/` — `observations.jsonl` writer (O_APPEND
  per-line atomic) + Anthropic env scrub.
- ✅ `internal/prompts/` — //go:embed defaults + 3-layer override
  resolver. Template.Version is sha256[:12] of body for cache
  pinning.
- ✅ `internal/provider/claudecli/` — Option A′ subprocess provider
  + Limiter (10 calls/session, 30/hour, 3-failure breaker, 15min
  cooldown).
- ✅ `bough observer run-once` — synchronous single-shot extraction
  pass with `--dry-run` preview.
- ✅ `bough instinct status / list / show` — read-side corpus
  inspection (5-bucket confidence histogram, filterable list).
- ✅ `bough doctor` — continuous-learning posture block (claude CLI
  on PATH, Anthropic env scrub warning, Limiter defaults,
  homunculus root).

v0.9.1 + v0.9.2 (= upcoming):

- 5-gate evolve pipeline (= ECC v3 verbatim, MEMBER_MIN=2 / COH_MIN
  =0.20 / LEXICON_COVERAGE_MAX=0.55 / REL_ISOLATION_MIN=0.40) +
  GATE 5 LLM judge + cluster-labels.json + SKILL.md / agent /
  command emit.
- `bough inject-context` UserPromptSubmit hook (9.5KB cap +
  confidence-sorted LRU) + SessionEnd handlers (summary /
  evaluate / evolve-claudemd) + PreCompact + optional observer
  daemon + `bough ecc import` migration.

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

## v0.8.0 — Evaluator adapters + global hook scope (shipped 2026-06-23)

Bundles two original roadmap lanes:

- **P5 (= evaluator adapters)** — `internal/evaluators/` ships
  GEPA / TextGrad / MUSE / SkillAudit in-process behind the v0.5
  `plugins/evaluator/api.SkillEvaluator` contract. The four
  strategies cover the research-paper-derived heuristics the
  roadmap has been carrying since v0.5.
- **P6 (= global hook scope)** — `--scope=user` on the
  `bough hook` family reaches `~/.claude/settings.json` so an
  operator can wire bough's observer once at the user level.

Memory-backend Store loop (= P4) stays deferred per the operator's
"memory backend は後回し" direction. v0.8 ships the read-side of
every pipeline that needs it; the persistent write side lands
once Letta + mem0 reconcile are ready.

The 12-language rule pack from the original v0.9 plan is now an
operator deliverable: pipe each language's idioms into
`bough ecc import` (= v0.7.2) and let the evaluator adapters
shape the surviving set. bough OSS stays language-neutral.

## v0.7.2 — ECC compat + dogfooding bridge (shipped 2026-06-23)

Reads the upstream affaan-m/everything-claude-code corpus so a
project with 300+ pre-existing instincts can land in bough's
schemas without re-running the evolve pipeline. Quality-gate
dispatch wires onto the v0.7.0 hook handle path.

- ✅ `bough ecc import` walks `~/.local/share/ecc-homunculus/`
  + each `projects/<id>/` and projects instincts / skills /
  agents / commands onto pkg/schema types. Verified against the
  local threecorp dogfood corpus: 1072 instincts / 48 skills /
  6 agents / 116 commands migrate cleanly.
- ✅ `internal/ecc/` parser handles the four ECC shapes (= YAML
  frontmatter + body for instincts / skills / agents; body-only
  with `Evolved from instinct:` line for commands). Catalog
  files (`INSTINCTS.md` / `MEMORY.md`) skip silently.
- ✅ Quality-gate dispatch in `bough hook handle` — runner
  framework lifted from v0.7.1 P1.5 + wired into the
  PostToolUse path through `.bough.yaml`'s `quality_gates:`
  section.
- ✅ Soft-error reporting + sample manifest +
  `--json instincts.json/skills.json/agents.json/commands.json`
  outputs so downstream tooling can pipe the projection.

Deferred to v0.8 (= P4): the ECC → memory backend Store loop.
v0.7.2 ships the read + project pipeline; the persistent write
side waits on Letta + mem0 reconcile.

## v0.7.1 — Evolve + LLM judge (shipped 2026-06-23)

Layered on top of the v0.7.0 safety floor. Round 5 reviewer
non-negotiables shaped the sprint: cache + audit + replay are all
mandatory before any LLM-touching surface ships, and the
CLAUDE.md write path stays opt-in via `--apply`.

- ✅ `JudgeClient` interface at `plugins/capability/api/llm.go`
  with three reference impls in `internal/judge/` (Heuristic
  default, Replay for golden corpus, Claude stub deferred to
  v0.7.2)
- ✅ 4-gate Go port (`internal/evolve/`) — Gate 1 schema, Gate 2
  heuristic filter, Gate 3 Jaccard clustering, Gate 4 candidate
  stamping. Thresholds match the ECC Python v3 canonical values.
- ✅ SHA256 cache key + audit dir (`.evolve/judgements/`) with
  atomic tmp+rename writes and `CachedJudge` read-through wrapper.
- ✅ JSON schema validation on every `JudgeVerdict` so an invalid
  LLM response falls through to DOUBT instead of poisoning the
  cache.
- ✅ `bough bootstrap --apply` pipeline — runs the evolve pipeline
  on `.bough/observations.jsonl` and writes PASS candidates into
  `.claude/skills/<label>.md` atomically. Refuses on dirty
  `.claude/` (= `--force` overrides); FAIL always skipped; DOUBT
  requires `--force`.
- ✅ Quality-gate framework (`internal/qualitygate/`) + new
  `quality_gates:` root section in `.bough.yaml`. Runner ships;
  PostToolUse dispatch wires in v0.7.2.
- ✅ Golden corpus at `internal/evolve/testdata/golden/` — Go-vs-
  Go regression baseline; Python v3 parity diff deferred to v0.7.2
  when `bough ecc import` lands.
- ✅ `bough hook replay --fixture -` supports stdin (= k smoke
  finding from v0.7.0 post-ship).
- ✅ `Makefile build` covers all 8 binaries (= v0.7.0 hotfix
  d25ee97).

## v0.7.0 — Bootstrap safety floor (shipped 2026-06-23)

The "automation is safe to turn on" floor. Nothing in this release
calls an external LLM; every artifact lands in a reviewable form
before touching the memory backend. Round 5 review (= 2026-06-22,
two independent external AI passes) split the LLM-touching surface
into v0.7.1 and front-loaded the safety + observability surfaces
into v0.7.0. The eight sub-phases all shipped:

- ✅ O-1.1 cobra surface skeleton (`bough hook`)
- ✅ O-1.2 install / uninstall / list reconciliation
- ✅ O-1.3 replay harness + canonical testdata fixtures
- ✅ O-1.4 `bough doctor` body + top-level alias
- ✅ O-1.5 `bough bootstrap --dry-run` → `.bough/proposals/<ts>/*.md`
- ✅ O-1.6 `bough hook handle` (= raw event capture into
  `.bough/observations.jsonl`)
- ✅ O-1.7 MCP write hardening (rate-limit + scope boundary +
  append-only audit; host wires worktree-only / 60-per-min /
  `.bough/memory/mcp_audit.jsonl` defaults when `--allow-write` on)
- ✅ O-1.8 end-to-end integration test in `conformance/hooks/`

## v0.7 — Bootstrap layer (round 5 refined)

The v0.6 retrospective (2026-06-22) clarified that the user-facing
intent — "`claude --worktree X` materialises an isolated dev
environment **and** generates the artifacts the next session will
need" — needs a dedicated layer above the existing CapabilityCompiler.
Round 5 external review (2026-06-22, two independent AI passes)
agreed on the direction but flagged the original 14-day v0.7.0 scope
as overambitious: LLM-judge inference + 4-gate clustering + hook
auto-wire + cost transparency in one sprint underestimates the
Bash/Python→Go rewrite cost. Both reviewers recommended splitting
the LLM-touching layer (= GATE 5) into v0.7.1 and front-loading the
safety + observability surfaces into v0.7.0 instead. The Phase split
below incorporates that guidance.

### v0.7.0 — Bootstrap safety floor (~10 day)

The "automation is safe to turn on" floor. Nothing here calls an
external LLM; every artifact lands in a reviewable form before
touching the memory backend.

- `bough hook install` / `uninstall` (= writes / removes the
  Claude Code `WorktreeCreate` / `PreToolUse` / `PostToolUse` /
  `UserPromptSubmit` / `SessionEnd` / `PreCompact` / `Stop`
  entries against `.claude/settings.json`), with idempotent
  reconciliation so re-running on a partially-wired monorepo
  converges instead of duplicating handlers.
- **`bough hook replay --event <name> --fixture <json>`** harness
  + golden tests over a `testdata/` corpus of canonical hook
  payloads. Hook auto-wire without a replay harness was named as
  the single highest carryover risk by both round 5 reviewers.
- `bough hook doctor` / **`bough doctor`** — surfaces the
  observer status, current hook wiring, per-worktree token /
  cost meter, and signing posture in a single command. Front-
  loaded into v0.7.0 (was v0.7.1 in the pre-review plan) so
  silent-billing or silent-observer regressions get caught the
  moment Bootstrap turns on.
- `bough bootstrap --dry-run` writes candidate artifacts to
  `.bough/proposals/<timestamp>/*.md` (= Markdown frontmatter,
  one file per candidate). The operator reviews with `git diff`
  semantics and runs `bough instinct approve <id>` to promote
  into the backend. The DB never sees an artifact the operator
  has not already inspected.
- Observer event capture: raw observations persist to
  `.bough/observations.jsonl` (= append-only, signed via the
  same provenance schema the capability artifacts use). No
  inference yet; ingestion remains opt-in.
- All generated artifacts ship `state: candidate`. Promotion
  stays a human CLI action (no MCP promote tool, no auto-active
  path).
- Schema additions kept "Letta interop-ready": every artifact
  records `source_trace`, `provenance.generated_by`, a
  git-backed export path, and a `scope_boundary` field so v0.8
  can light up Letta Context Repositories or Graphiti memfs
  without a schema migration.
- MCP write surface gains the round 5 mitigation set: dry-run
  default, per-tool permission flag, per-worktree scope
  enforcement, rate limit per session, append-only audit log,
  schema validation before store. `memory.promote` stays
  refused on the MCP surface — promotion is a CLI human
  action (round 5 unanimous).

### v0.7.1 — Evolve + LLM judge (~7 day)

Splits from v0.7.0 so the LLM-touching surface ships with its
own debugging budget.

- 4-gate mechanical filter (`/evolve-skill-manual-v3` algorithm)
  ported as a single Go pipeline. v3 is the canonical algorithm;
  the upstream ECC `/evolve` clustering acts as parser / baseline
  reference, not a parallel port.
- GATE 5 LLM judge behind a `JudgeClient` interface in
  `plugins/capability/api/llm.go`. Three implementations:
  - `ClaudeJudgeClient` (= live LLM, gated by config)
  - `HeuristicJudgeClient` (= deterministic fallback for CI /
    offline)
  - `ReplayJudgeClient` (= fixture / cassette playback so the
    integration tests are reproducible)
- SQLite-backed judge cache keyed by
  `sha256(prompt_version | model_id | cluster_member_ids |
  cluster_member_hashes | nearest_prior_label |
  nearest_prior_description)` so a re-run of the same evolve
  pass never re-bills the operator.
- Audit dir `.evolve/judgements/<cache_key>.json` storing
  model, prompt_version, request hash, raw response, parsed
  verdict, cost estimate, timestamp. Append-only.
- JSON-schema-validated judge output (verdict ∈
  {PASS, DOUBT, FAIL}, confidence, reason,
  recommended_label, reuse_prior_label). temperature = 0,
  max_output_tokens fixed.
- CLAUDE.md proposal pipeline (= observe → propose → apply)
  using the same judge interface so the heuristic / replay
  fallbacks cover it too.
- Quality-gate framework concept (= user supplies the lint /
  typecheck / smoke command, bough sequences it as a Post-tool
  hook).
- Golden corpus driven by threecorp's live 346-instinct /
  21-skill / 6-agent / 116-command snapshot so the Go port's
  output diff-tests against the in-production Python output.

### v0.7.2 — ECC compatibility + dogfooding (~5 day)

- `bough ecc import` reads `~/.local/share/ecc-homunculus/
  projects/<id>/` and emits the canonical bough schema.
- Round-trip validation against the threecorp dogfooding
  corpus (= the same 346 / 21 / 6 / 116 the v0.7.1 judge
  golden tests use).
- Bug-fix budget reserved for whatever the dogfooding session
  surfaces.

### v0.7.3 — Polish (~3 day)

- `README-ja.md` (= threecorp / eiicon-company devs read the
  Japanese tagline first).
- `examples/` pack: a curated subset of upstream skills + threecorp
  commands so a first-time user sees what the bootstrap surface
  can actually emit. The full upstream catalogue stays out of the
  binary release.

### Reframed v1.0 / v2.0 vision (round 5 Q7)

Both round 5 reviewers warned against pivoting bough into a
multi-agent orchestrator (claude-flow / CrewAI territory) or a
generic memory database. The competitive moat is "worktree-native
AI development environment orchestrator" — keep it.

- **v1.0** stabilises the v0.7 surfaces (= hook install / replay,
  observer, candidate generation, `bough doctor`, MCP candidate
  tools, evolve-v3, ECC import) under semver guarantees.
- **v2.0** unlocks Letta Context Repositories interop,
  `SkillEvaluator` plus GEPA / TextGrad / MUSE-Autoskill adapters,
  signed skill registry, evaluator-driven skill retirement, and
  multi-host backends (Cursor / OpenCode / Codex). The CI-tier
  pivot (= "instincts that pass CI promote to global") becomes
  defensible only after the evaluator layer ships.

## What bough deliberately does NOT do

- weight updates (SEAL / SFT / RLHF) — model-tier concern, not orchestration
- `instinct → skill → command → agent` as a forced single chain — round 1 rejected the **chain** in favour of a parallel compile target set. This rejection is a Layer C decision and is **orthogonal** to the 2026 Layer A (memory CRUD) and Layer B (skill execution) anti-pattern literature; see `docs/CONCEPTS.md` for the three-layer split. The seven parallel targets (memory, rule, skill, command, tool, agent, evaluator) all remain valid sinks; the InstinctCandidate metadata picks which subset to materialise.
- proprietary vendor memory (OpenAI Memory, Anthropic Memory) — vendor lock-in
- GPL/AGPL backends — license drift for downstream MIT/Apache users

## Why these choices

The full design history lives in the round 1 / 2 / 2.5 / 3 synthesis notes (see PR #N). The recurring thread: **bough is a per-worktree memory orchestration layer, not an agent memory system.** Every choice on this roadmap reinforces that boundary.
