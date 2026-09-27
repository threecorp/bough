---
description: Check a worktree's engine and ports: entries against the declared ranges, and that its .env.local files exist.
argument-hint: <worktree-name>
allowed-tools: Bash(bough:*)
---

Run `bough verify $ARGUMENTS`. For each engine and each `ports:` entry in
`.bough.yaml` it reports a registry entry that is missing or outside its
declared range, and it reports a `.env.local` that should exist but does not.
It does not compare the values inside `.env.local`, and registry entries that
`.bough.yaml` no longer declares are not checked.

If `$ARGUMENTS` is empty, run `bough list` first and ask which worktree to
verify. Report whether it is consistent, and if `bough verify` exits non-zero,
quote each drift line.
