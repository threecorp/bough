---
name: using-bough
description: How to drive bough from a Claude Code session — creating and tearing down per-worktree isolated dev environments (isolated MySQL/Redis/Elasticsearch per branch). Use when the user wants an isolated environment for a branch, or asks about bough worktrees, ports, or engines.
---

# Using bough from Claude Code

`bough` is a CLI that bootstraps a per-worktree isolated dev environment
(deterministic ports + its own MySQL / Redis / Elasticsearch / … engines +
a rendered `.env.local` in every sub-repo) declared in a monorepo's
`.bough.yaml`.

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
| confirm the hook wiring and plugin posture | `/bough:doctor` |
| validate a `.bough.yaml` | `/bough:config-validate` |

The primary way to create a worktree is still `claude --worktree <name>`, which
fires a `WorktreeCreate` hook — wired either by the `bough-hooks` / `bough-all`
plugin or by `bough claude hook install` (see below). `/bough:create` cuts one
from inside an already-running session.

## Hook wiring

This skill and the `/bough:*` commands act only when they are invoked, so they
have no background side effects wherever they are installed.

Whether hooks come with them depends on the variant: **`bough`** ships commands
+ this skill and no hooks, while **`bough-hooks`** and **`bough-all`** also ship
the `WorktreeCreate` / `WorktreeRemove` wiring. With one of those two installed,
do NOT also run `bough claude hook install` — both fire, and one
`claude --worktree` runs `bough create` twice. `bough claude doctor` says which
is in effect.

With the hookless `bough` variant, the `WorktreeCreate` / `WorktreeRemove`
handlers are wired separately, scoped to the repo the user actually wants
`claude --worktree` to work in:

- `bough claude hook install --scope project` — wire this repo's `.claude/settings.json` (recommended)
- `bough claude hook install --scope user` — wire `~/.claude/settings.json` (every repo)
- `bough claude hook uninstall` — remove them

If the user asks to "make `claude --worktree` work here", point them at
`bough claude hook install --scope project`.

## Upgrading from v0.27.0 or earlier

Those versions also wired six events for a continuous-learning loop that no
longer exists. They now do nothing, and `bough claude doctor` names any that
are still in `settings.json`. One `bough claude hook install` removes them.
