# bough roadmap

bough bootstraps one isolated development environment per git
worktree: the worktree itself, an engine set of its own, deterministic
ports, a rendered `.env.local` in every sub-repo, and the two
`claude --worktree` hooks that drive it. This document is the
canonical reference for that scope; the release CHANGELOG ties
specific commits back to each item.

## v0.28.0 — Isolation only (2026-09)

v0.9.0 through v0.27.0 carried a second, unrelated subsystem: a
continuous-learning loop (observe → instinct → judge → evolve →
inject) with its own corpus under `~/.local/share/bough-homunculus`,
its own `claude --print` calls, and six extra Claude Code hook events.

v0.28.0 removes all of it. Learning is a Claude Code plugin's job, and
an isolation tool has no business shipping a second implementation of
one. What remains is the isolation core, which is what bough was for
before v0.9.0 and what it is used for today.

Concretely, v0.28.0:

- wires two hook events (`WorktreeCreate`, `WorktreeRemove`) instead
  of eight, and prunes the other six out of `settings.json` on the
  next `bough claude hook install`;
- drops the `instinct:`, `quality_gates:`, `memory_backends:` and
  `export:` sections from `.bough.yaml` — they are read and warned
  about for one minor series, then stop parsing in v0.29.0;
- leaves `~/.local/share/bough-homunculus` on disk untouched (doctor
  only reads its `observer.pid` files); deleting it is the operator's call.

`docs/attic/` keeps the design notes. Pin `v0.27.0` to keep the loop.
`docs/MIGRATION-v0.27-to-v0.28.md` has the upgrade steps.

## v0.27.0 — Docker is the only engine backend (2026-09)

The Nix / services-flake engine backend is gone: it could not start
Elasticsearch, gave Postgres different credentials than the container
path, and no CI job ever ran it. `engines[].backend` accepts only
`docker` and may be omitted. Each engine honours `engines[].version` or
refuses it at `Up`, each plugin registers its backend in `New()`, and
`bough remove` deletes nothing while an engine port still answers.
The CHANGELOG has the detail.

## v0.5.0 - v0.8.0 — Superseded memory-orchestration surface

v0.5.0 through v0.8.0 (June 2026) built a `MemoryBackend` /
`InstinctMinter` plugin architecture — pluggable SQLite / mem0 /
Graphiti backends, a `CapabilityCompiler` that materialised instincts
into memory / rule / skill / command / tool / agent / evaluator
artifacts, a read-only `bough-mcp-server`, `SkillEvaluator` adapters
(GEPA / TextGrad / MUSE / SkillAudit), and a "v0.7 Bootstrap" plan for
LLM-judged clustering on top of it. v0.9.0 reset all of it in favour
of the continuous-learning port described above; none of it shipped
past v0.8.1, and v0.28.0 retired the port too. See CHANGELOG.md for
the release-by-release detail if you need the history.

## What bough deliberately does not do

These are durable non-goals:

- Agent memory of any shape — instinct corpora, observation logs,
  prompt injection into the next session. Retired in v0.28.0, and a
  Claude Code plugin's job rather than an isolation tool's.
- Weight updates (SEAL / SFT / RLHF) — a model-tier concern, not
  something an orchestration layer does.
- Proprietary vendor memory (OpenAI Memory, Anthropic Memory) —
  avoids vendor lock-in.

bough is a per-worktree development-environment orchestrator. These
non-goals are what keep it one.
