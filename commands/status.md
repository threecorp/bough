---
description: Show each registered port and whether something is listening on it.
allowed-tools: Bash(bough:*)
---

Run `bough status` and summarize the worktree/port table for the user. Call out
any registered port that is not listening: its engine or app is not running.
`status` only probes registered ports, so it cannot see other listeners.
