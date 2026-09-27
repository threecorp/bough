---
description: Show each registered port and whether something is listening on it.
allowed-tools: Bash(bough:*)
---

Run `bough status` and summarize the worktree/port table for the user. Call out
any registered port with no listener detected: the engine or app on it is
probably not running. The probe uses `lsof`; if `lsof` is missing or not
permitted, every port reads as not listening. `status` only probes registered
ports, so it cannot see other listeners.
