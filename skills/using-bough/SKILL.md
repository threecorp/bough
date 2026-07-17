---
name: using-bough
description: How to drive bough from a Claude Code session — creating/tearing down per-worktree isolated dev environments (isolated MySQL/Redis/Elasticsearch per branch) and inspecting the continuous-learning instinct corpus. Use when the user wants an isolated environment for a branch, asks about bough worktrees/ports/engines, or wants to see or evolve learned instincts.
---

# Using bough from Claude Code

`bough` is a CLI that bootstraps a per-worktree isolated dev environment
(deterministic ports + its own MySQL / Redis / Elasticsearch / … engines +
a rendered `.env.local` in every sub-repo) declared in a monorepo's
`.bough.yaml`, and runs a continuous-learning loop (observe → instinct →
evolve) over Claude Code session events.

This plugin ships the commands, **not the binary**. Every command below shells
out to `bough` on `PATH`.

## Preflight: is the binary installed?

Before the first bough command in a session, confirm the binary is reachable:

```bash
command -v bough
```

If it is missing, do not guess — tell the user to install it first (GitHub
release tarball, `nix profile install github:threecorp/bough`, or `go install`;
see the bough README) and stop. The plugin's markdown commands cannot function
without it.

## When to reach for which command

| The user wants to… | Use |
|---|---|
| spin up an isolated env for a branch | `/bough:create <name>` |
| tear one down (keeps the branch) | `/bough:remove <name>` |
| see what worktrees/ports exist | `/bough:list`, `/bough:status` |
| check a worktree for drift | `/bough:verify <name>` |
| confirm hooks/observer/cost posture | `/bough:doctor` |
| see learned instincts | `/bough:instinct-status`, `/bough:instinct-list` |
| share instincts across projects | `/bough:instinct-promote` |
| turn instincts into skills/commands | `/bough:evolve` |
| validate a `.bough.yaml` | `/bough:config-validate` |

The primary way to create a worktree is still `claude --worktree <name>`, which
fires a `WorktreeCreate` hook — but that hook is wired by `bough hook install`
(see below), not by this plugin. `/bough:create` cuts one from inside an
already-running session.

## Hook wiring (not done by this plugin)

This plugin ships **commands + this skill only — no hooks**. It does not observe,
inject, or run any lifecycle handler on its own; the `/bough:*` commands act only
when the user invokes them. So installing the plugin (even user-scoped, in every
repo) has no background side effects.

The observe → instinct → inject → evolve → preserve loop, and the
`WorktreeCreate` / `WorktreeRemove` handlers, are wired separately and scoped to
the project the user actually wants observed:

- `bough hook install --scope project` — wire this repo's `.claude/settings.json` (recommended)
- `bough hook install --scope user` — wire `~/.claude/settings.json` (observe every repo)
- `bough hook uninstall` — remove them

If the user asks to "turn on learning / observation here", point them at
`bough hook install --scope project`. LLM instinct minting stays opt-in on top of
that (`bough instinct observer start`, or `.bough.yaml` `instinct.observer.autostart`).
