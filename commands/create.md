---
description: Create a bough per-worktree isolated dev environment (starts its engines, renders .env.local for repos that declare env_local).
argument-hint: <worktree-name>
allowed-tools: Bash
---

Create a bough worktree named `$ARGUMENTS`.

If `$ARGUMENTS` is empty, ask the user for the worktree name (e.g. `F-Feature`) and stop.

Run it from the monorepo root:

```bash
bough create --name "$ARGUMENTS" --cwd "$PWD"
```

Then:

- Report the worktree root path bough printed on stdout.
- Read stderr. A `[bough] WARNING: create finished with N problem(s)` block
  means the worktree exists but a sub-repo, `.env.local` or `post_create`
  step failed; quote each listed problem instead of reporting success.

If the command fails with "command not found: bough", stop and tell the user the
`bough` binary must be installed on PATH first — point them at the install
section of the bough README (the plugin ships the commands, not the binary).
