---
description: Cluster the instinct corpus into skills / agents / commands via bough's 5-gate evolve pipeline.
argument-hint: "[--generate]"
allowed-tools: Bash(bough:*)
---

Run `bough instinct evolve $ARGUMENTS` to run the 5-gate pipeline that clusters instincts
into candidate skills / agents / commands.

If the user wants to actually emit artifacts, use `bough instinct evolve --generate`. Note
that GATE 5 runs an LLM judge through the operator's own Claude Code subscription
(no API key, no separate billing). Report which skills / commands / agents were
emitted and where they were written.
