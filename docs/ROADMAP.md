# bough roadmap

bough bootstraps one isolated development environment per git
worktree: the worktree itself, an engine set of its own, deterministic
ports, a rendered `.env.local` per sub-repo that declares `env_local`, and the two
`claude --worktree` hooks that drive it. The CHANGELOG records what
each release shipped; this file lists what is planned and what is out
of scope.

## Next

- **v0.29.0** — drop the v0.28.0 compatibility shims: a retired
  `.bough.yaml` section (`instinct:`, `quality_gates:`,
  `memory_backends:`, `export:`) becomes a validation error, and a
  retired hook event makes `bough hook handle` exit non-zero. See
  [MIGRATION-v0.27-to-v0.28.md](MIGRATION-v0.27-to-v0.28.md).
- **Multi-port engines in the host** — the plugin contract already
  carries one port per role; the host still allocates and passes only
  `main`. Per-role allocation comes before any bundled rabbitmq / kafka /
  NATS plugin.

## Out of scope

- Agent memory or learning of any kind (observation logs, instinct
  corpora, prompt injection). bough carried one from v0.9.0 to v0.27.0
  and removed it; use a Claude Code plugin built for that.
- Running engines anywhere but on the developer's machine.

bough is a per-worktree development-environment orchestrator; these
limits are what keep it one.
