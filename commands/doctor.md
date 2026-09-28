---
description: Report bough's hook wiring, worktree containers, and engine-plugin posture (transparency check).
allowed-tools: Bash(bough:*)
---

Run `bough claude doctor` and summarize for the user:

- which hook events are wired, and whether they come from bough or a hand-edit;
- whether any worktree container is in a shape the host would refuse
  (`bough repair` converts them);
- which `bough-plugin-*` engine plugins were found on `PATH` and how many of
  them actually start;
- whether wiring or `.bough.yaml` sections from a retired feature are still
  there, and whether an `observer.pid` from the old observer daemon names a
  live process (doctor cannot tell whether that process is the daemon; pass
  on its `pgrep` check).

If the report warns that the hooks are wired twice (in `settings.json` and by
the bough Claude Code plugin), say that one copy has to go, and pass on the
remedy the report prints.

For retired wiring or config, pass on the remedy the report prints; the
upgrade guide is `docs/MIGRATION-v0.27-to-v0.28.md` in the bough repository.
