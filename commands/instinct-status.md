---
description: Print per-project instinct totals + a confidence histogram from the bough corpus.
allowed-tools: Bash(bough:*)
---

Run `bough instinct status` and summarize the learned-instinct corpus for the
user: per-project totals and the confidence distribution. If the corpus is
empty, note that instincts appear once `bough instinct observer run-once` (opt-in) has
minted some from captured session observations.
