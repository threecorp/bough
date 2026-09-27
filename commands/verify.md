---
description: Check a worktree's registered ports against the declared ranges and that its .env.local files exist.
argument-hint: <worktree-name>
allowed-tools: Bash(bough:*)
---

Run `bough verify $ARGUMENTS`. It reports a port missing from the registry, a
port outside its declared `.bough.yaml` range, and a `.env.local` that should
exist but does not. It does not compare the values inside `.env.local`.

If `$ARGUMENTS` is empty, run `bough list` first and ask which worktree to
verify. Report whether it is consistent, and if `bough verify` exits non-zero,
quote each drift line.
