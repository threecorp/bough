# bough conceptual model — three layers

> **bough is not an agent memory system. bough is a per-worktree memory orchestration layer.**

This document pins where bough sits among the 2026 agent memory / skill
ecosystem so reviewers, plugin authors, and future contributors can
avoid the layer-confusion that the maintainers themselves hit during
v0.6 dogfooding (2026-06-22).

## TL;DR

bough operates in **layer C (artifact compile chain)**. Layer A and
Layer B are delegated to external providers:

| Layer | Concern | bough position | Delegated to |
|---|---|---|---|
| **A. Memory architecture** | short / long / archival memory hierarchy, CRUD policy | not bough's job | mem0, Graphiti, Letta, SQLite reference-fallback (= bough plugins) |
| **B. Skill execution orchestration** | runtime invocation of skills (flat / DAG / sequential trajectory) | not bough's job | the host AI (Claude Code, Cursor, Anthropic Skills runtime) |
| **C. Artifact compile chain** | translating an InstinctCandidate into a {memory, rule, skill, command, tool, agent, evaluator} artifact | **this is bough** — `CapabilityCompiler` + Emitter registry | builtin emitters (`agent-skill`, `claude-skill`, `mcp`); v0.6.x plugin slots for community emitters |

A common misreading (one the maintainers also made in v0.6 reviews)
is to treat the 2026 arXiv anti-pattern literature on memory and
execution as criticism of bough's compile design. It is not. The
anti-pattern papers — SkillX, GraSP, AgentSkillOS, AtomMem, PSN —
critique Layer A (memory CRUD workflows) and Layer B (flat skill
libraries / sequential trajectory execution). Bough's
`instinct → {memory, rule, skill, command, tool, agent, evaluator}`
is a Layer C parallel-compile-target dispatch, not a forced chain
in any of those layers.

## Why this matters

### Layer A is mem0 / Graphiti / Letta territory

bough does not own a memory store. The SQLite reference-fallback that
ships in v0.5 is exactly that — a fallback so the system has a
deterministic Read path when the configured external backend is down.
Memory architecture decisions (verbatim storage vs filesystem-backed
vs knowledge graph vs binary embedding, eviction policy, archival
promotion) live entirely inside the chosen `MemoryBackend` plugin.

bough's Layer A surface is:

- `MemoryBackend` interface (7 RPCs frozen in v0.5)
- `Capabilities` advertise (17 fields in v0.6 so the host can
  caps-gate behaviour without faking)
- `instinct.fallback_on_error` for split-brain-safe Read degradation
  (v0.5.1)
- namespace mapping rules (`docs/NAMESPACE_MAPPING.md`)
- the canonical Scope tier model (worktree / repo / global)

### Layer B is host AI territory

bough does not invoke skills at runtime. When `bough capability
compile --to claude-skill` writes a `SKILL.md`, the file lives where
Anthropic's Skills runtime expects (typically `~/.claude/skills/`),
and Claude Code decides when / how to load it. Likewise, MCP bundles
emitted by `--to mcp` are consumed by whichever MCP client the
operator points at.

bough's Layer B surface is just `bough-mcp-server` for the read-only
side (memory.query Tool + bough://memory/scopes Resource, MCP
spec_version 2025-11-25). The state-changing path (`memory.store` /
`memory.forget` behind `--allow-write`) lands in v0.6.x.

### Layer C is where bough actually lives

`CapabilityCompiler` walks `instincts × kinds × targets`, stamps a
deterministic Checksum, and dispatches through the Emitter registry.
The 7 kinds — memory, rule, skill, command, tool, agent, evaluator —
are **parallel compile targets**, not a chain. An instinct compiles
to exactly the artifacts its trigger / contract / validation
metadata warrants:

| Trigger (round 1-2 synthesis L108-118 of `bough-instinct-primitive-syntheses-round2.md`) | Target |
|---|---|
| 1-2 observations of a small fact | memory / rule |
| 3+ observations of a natural-language pattern | skill |
| sequence with explicit preconditions and a terminating step | command |
| deterministic, side-effect-bounded primitive | tool / MCP |
| domain wide enough to warrant its own context budget | agent |
| measurable success condition | evaluator |

There is no implied order; the host can compile any subset in any
order, and an InstinctCandidate is free to materialise as zero, one,
or several artifacts depending on what its metadata says.

## What "forced single chain rejected" actually means

`docs/ROADMAP.md` records that round 1 rejected
`instinct → skill → command → agent` as a forced single chain. This
rejection is a **Layer C choice**, settled before bough wrote a single
line of CapabilityCompiler code:

- A forced chain says "every instinct must become a skill before it
  can become anything else, and every skill must become a command,
  …". That model is rigid, makes the host re-implement four
  artifact lifecycles end-to-end, and pretends instinct → skill is
  the only valid edge.
- The parallel compile target says "an instinct is the source-of-
  truth; the compiler picks which subset of {memory, rule, skill,
  command, tool, agent, evaluator} to materialise based on the
  instinct's metadata". One source, many sinks.

Either way, the choice is internal to Layer C and orthogonal to
anything happening in Layer A or Layer B.

## Where ECC / continuous-learning-v2 fits

`affaan-m/everything-claude-code` (ECC) and its
`continuous-learning-v2` skill are **layer C systems** too, with a
Claude Code-native hook-driven trigger model. ECC's pipeline is:

```
PreToolUse / PostToolUse observations → confidence-scored instincts
       → cluster (≥2 / ≥3 with thresholds)
       → evolve into  skill  /  command  /  agent   ← slash-branched OR, not chain
       → install into ~/.claude/skills, ~/.claude/commands, ~/.claude/agents
```

Notice the `skill / command / agent` is an OR branch, not a chain.
ECC and bough land at the same Layer C answer (`parallel compile
target`) via different design routes. The relevant difference is
**how the trigger fires**: ECC uses Claude Code's hook surface
(PreToolUse / PostToolUse / SessionEnd / PreCompact / etc.) so the
pipeline runs continuously; bough's `instinct ingest` / `instinct
mint` are explicit one-shot CLI invocations the operator (or a hook
the operator wires up) calls.

For the threecorp deployment (which forks and improves on ECC) the
practical answer is: ECC handles the hook-driven continuous learning,
bough handles the worktree-isolated infrastructure + per-worktree
DB engines + (optionally) external memory backend orchestration. The
two systems coexist and do not duplicate each other.

The v0.7 Bootstrap layer (see ROADMAP) is where bough opens the door
to an internal hook trigger model — a `bough init` invocation that
fires when `claude --worktree` first opens a worktree and lets a
configured Bootstrap Agent populate the seven artifact kinds in
parallel against the freshly-materialised sub-repos. This is
optional, opt-in, and never forced; it is the first step of merging
ECC's continuous-learning ergonomics with bough's worktree-isolated
infrastructure surface.

## Where Anthropic / Letta / MCP fit

- **Anthropic SKILL.md** is a Layer C *output format*. bough's
  `claude-skill` emitter writes it. Reading it is the Claude Skills
  runtime's job (Layer B).
- **Letta Context Repositories (2026-02-12)** is a Layer A choice —
  git-backed memory filesystem with worktree-isolated subagents and
  sleep-time defragmentation. bough could wrap a Letta `MemoryBackend`
  plugin in v0.7 the same way it wrapped mem0 in v0.6.
- **MCP (Model Context Protocol, spec 2025-11-25)** is a Layer B
  transport. bough's `mcp` emitter writes tool / resource / prompt
  bundles in this format; `bough-mcp-server` speaks the protocol so
  Claude Desktop and Cursor can read bough's memory subsystem.

## Reference

- Layer A anti-pattern critique: AtomMem (`arXiv:2601.08323`,
  Jan 2026 revised Mar 2026).
- Layer B anti-pattern critique: SkillX (`arXiv:2604.04804v2`,
  Apr 2026), GraSP (`arXiv:2604.17870v1`, Apr 2026), AgentSkillOS
  (`arXiv:2603.02176v1`, Feb 2026), PSN (`arXiv:2601.03509v1`,
  Jan 2026).
- bough Layer C synthesis: `bough-instinct-primitive-syntheses.md`
  (round 1) and `bough-instinct-primitive-syntheses-round2.md`
  (round 2 / 2.5) for the chain-rejection trace; full file list
  in handover notes alongside the v0.6 retrospective.
- ECC / continuous-learning-v2: `github.com/affaan-m/everything-
  claude-code` and `github.com/affaan-m/ECC`.
- Letta Context Repositories:
  `https://www.letta.com/blog/context-repositories/`.
- Anthropic Skills: `https://www.anthropic.com/engineering/
  equipping-agents-for-the-real-world-with-agent-skills`.
- MCP spec 2025-11-25: `https://modelcontextprotocol.io/
  specification/2025-11-25`.

## See also

- `docs/INSTINCTS.md` — v0.5 instinct subsystem lifecycle.
- `docs/CAPABILITY_COMPILER.md` — Layer C compiler internals.
- `docs/EXTERNAL_MEMORY_BACKENDS.md` — Layer A plugin authors guide.
- `docs/MCP_SERVER.md` — Layer B exposure surface.
- `docs/ROADMAP.md` — v0.5 → v0.6 → v0.7 plan.
