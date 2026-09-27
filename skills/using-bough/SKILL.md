---
name: using-bough
description: How to drive bough from a Claude Code session — creating and tearing down per-worktree isolated dev environments (their own database and search engines per branch). Use when the user wants an isolated environment for a branch, or asks about bough worktrees, ports, or engines.
---

# Using bough from Claude Code

`bough` is a CLI that bootstraps a per-worktree isolated dev environment
(deterministic ports, its own engines such as MySQL / Redis /
Elasticsearch, and a rendered `.env.local` for each sub-repo that declares
`env_local`) from a monorepo's `.bough.yaml`.

This plugin ships the commands, **not the binary**. Every command below shells
out to `bough` on `PATH`.

## Preflight: is the binary installed?

Before the first bough command in a session, confirm the binary is reachable:

```bash
command -v bough
```

If it is missing, do not guess — tell the user to install it first (GitHub
release tarball or `go install`; see the bough README) and stop.

## When to reach for which command

Command names depend on how bough was installed: `/bough:create` with the
`bough` plugin, `/bough-all:create` with `bough-all`, `/create` when installed
with `bough claude command install`.

| The user wants to… | Command |
|---|---|
| spin up an isolated env for a branch | `create <name>` |
| tear one down | `remove <name>` |
| see what worktrees/ports exist | `list`, `status` |
| check a worktree's ports and `.env.local` files | `verify <name>` |
| confirm the hook wiring and plugin posture | `doctor` |
| validate a `.bough.yaml` | `config-validate` |

The usual way to create a worktree is `claude --worktree <name>`, which fires
the `WorktreeCreate` hook. `create` makes one from inside a running session.

## Hook wiring

`bough-hooks` and `bough-all` ship the `WorktreeCreate` / `WorktreeRemove`
hooks; the `bough` plugin does not. With a hook-bearing plugin installed, do
NOT also run `bough claude hook install` — both fire, and one
`claude --worktree` runs create twice. `bough claude doctor` says which is in
effect.

Without one, wire the hooks from the repo the user wants
`claude --worktree` to work in:

- `bough claude hook install --scope project` — this repo's `.claude/settings.json` (recommended)
- `bough claude hook install --scope user` — `~/.claude/settings.json` (every repo)
- `bough claude hook uninstall` — remove them

Upgrading from v0.27.0 or earlier leaves six retired hook events behind;
`bough claude doctor` names them and prints the remedy.
