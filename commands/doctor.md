---
description: Report bough's hook wiring, worktree containers, and engine-plugin posture (transparency check).
allowed-tools: Bash(bough:*)
---

Run `bough doctor` and summarize for the user:

- which hook events are wired, and whether they come from bough or a hand-edit;
- whether any worktree container is in a shape the host would refuse;
- which `bough-plugin-*` engine plugins were found on `PATH` and how many of
  them actually start (nothing is reported when none are installed);
- whether wiring or `.bough.yaml` sections from a retired feature are still there.

If the report warns that these hooks live in `settings.json` while the bough
Claude Code plugin is also installed, explain that the two double-fire and the
`settings.json` copy should be removed with `bough claude hook uninstall`.

If it names retired wiring or retired config sections, say that one
`bough claude hook install` prunes the wiring, and the `.bough.yaml` sections
are the user's to delete. Two cases install does NOT cover, so pass them on as
the report words them: a retired entry sharing a hook group with one the user
wrote has to be deleted by hand, and wiring supplied by the `bough-hooks` /
`bough-all` plugin goes away with `claude plugin update`, not with install.
