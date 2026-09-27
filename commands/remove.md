---
description: Tear down a bough per-worktree environment (stops its engines, drops datadirs, removes the worktree).
argument-hint: <worktree-name-or-path>
allowed-tools: Bash
---

Tear down the bough worktree identified by `$ARGUMENTS` (a worktree name or an
absolute worktree path).

If `$ARGUMENTS` is empty, run `bough list` first, show the registered worktrees,
and ask the user which one to remove.

```bash
# when $ARGUMENTS is an absolute path:
bough remove --path "$ARGUMENTS"
# when $ARGUMENTS is a bare name (run from the monorepo root):
bough remove --name "$ARGUMENTS"
```

Check the exit status and stderr before reporting:

- If it refuses because an engine port still answers, quote that message. The
  datadir, worktree and registry entry were kept; the engines were already
  asked to stop.
- Quote any `worktree remove`, `branch -D` or `rm -rf` line on stderr: remove
  prints those and carries on, so the worktree may be partly left behind.
- Otherwise confirm the engines were stopped and the worktree removed.

The git branch is kept unless `.bough.yaml` sets `teardown.remove_branch: true`.
