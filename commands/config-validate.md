---
description: Validate a .bough.yaml against the bough schema.
argument-hint: "[path-to-.bough.yaml]"
allowed-tools: Bash(bough:*)
---

Run `bough config validate $ARGUMENTS` and report whether the config is valid.
With no path it reads `.bough.yaml` in the current directory, so run it from
the monorepo root or pass the path. If it fails, quote the exact schema error
and point at the offending key. Quote any `WARNING` lines too.
