# Attic

Design docs for surfaces bough once shipped and no longer does. They
stay here as design history rather than in git log alone. Nothing in
this directory describes the current binary.

## The v0.9-v0.27 continuous-learning loop

v0.9.0 through v0.27.0 layered an agent-memory loop on top of the
isolation core: session observations under
`~/.local/share/bough-homunculus/`, confidence-scored instincts minted
through `claude --print`, a five-gate clustering pipeline that emitted
skills / agents / commands, and six extra Claude Code hook events.
v0.28.0 removed all of it — learning belongs to a Claude Code plugin,
not to an isolation tool. Pin **v0.27.0** for the working
implementation; [docs/MIGRATION-v0.27-to-v0.28.md](../MIGRATION-v0.27-to-v0.28.md)
has the upgrade steps.

- [EVOLVE.md](EVOLVE.md) — the five-gate instinct → skill / agent /
  command pipeline.
- [QUARANTINE-REVIEW.md](QUARANTINE-REVIEW.md) — the review flow for
  instincts held back by the denylist.
- [denylist.template.txt](denylist.template.txt) — the starting
  denylist that flow was built around.

## The v0.5-v0.8 memory-orchestration surface

v0.5.0 through v0.8.0 built a `MemoryBackend` / `InstinctMinter`
plugin architecture — mem0 / Graphiti backends, a
`CapabilityCompiler`, `bough-mcp-server`, evaluator adapters. v0.9.0
superseded it wholesale, and v0.28.0 then retired its successor too.
There is no plan to build these backends; pin **v0.8.1** if you need
the working implementation they describe.

- [BACKENDS.md](BACKENDS.md)
- [CAPABILITY_COMPILER.md](CAPABILITY_COMPILER.md)
- [CONCEPTS.md](CONCEPTS.md) — the v0.5-v0.8 three-layer model (Layer A memory backends / Layer B skill execution / Layer C `CapabilityCompiler`).
- [EXTERNAL_MEMORY_BACKENDS.md](EXTERNAL_MEMORY_BACKENDS.md)
- [INSTINCTS.md](INSTINCTS.md) — the v0.5-v0.8 instinct subsystem (`memory_backends:` config, `bough instinct mint/approve/promote/query/forget`). The v0.9-v0.27 `bough instinct status/list/show/promote` shared the `promote` name but was a different command over a different data model.
- [MCP_SERVER.md](MCP_SERVER.md)
- [MEMORY_PLUGIN_AUTHOR_GUIDE.md](MEMORY_PLUGIN_AUTHOR_GUIDE.md)
- [NAMESPACE_MAPPING.md](NAMESPACE_MAPPING.md)
