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

If it refuses because an engine port still answers, quote that message: nothing
was deleted. Otherwise confirm the engines were stopped and the worktree
removed. The git branch is kept unless `.bough.yaml` sets
`teardown.remove_branch: true`.
