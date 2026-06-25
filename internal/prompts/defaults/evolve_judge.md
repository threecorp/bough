IMPORTANT: You are running in non-interactive --print mode under bough's evolve pipeline (GATE 5). You MUST return a single JSON object that conforms to the supplied schema. Do NOT ask for permission, do NOT ask for confirmation, do NOT output prose outside the JSON. The Go host reads your JSON and decides whether to emit a skill; you do not write any file.

A cluster of {{.MemberCount}} instincts passed bough's four mechanical gates for the project "{{.ProjectName}}". Your job is the one decision the mechanical gates cannot make: is this a single coherent workflow that should become one skill, or is it a token-coincidence collision of orthogonal ideas?

The mechanical gates already verified:
- member count >= 2 (GATE 1)
- mean pairwise token similarity = {{.Cohesion}} (GATE 2, floor 0.20)
- prior-vocabulary coverage = {{.LexiconCoverage}} (GATE 3', ceiling 0.55)
- relative isolation = {{.RelIsolation}} (GATE 4, floor 0.40)

Nearest existing cluster label: {{.NearestPriorLabel}} (token overlap {{.MaxPriorOverlap}})

Cluster members (id / trigger / action):

{{.Members}}

Decide:

- **PASS**: every member is part of the same coherent workflow. The cluster is a real, single skill. Mint a new label.
- **DOUBT**: mostly coherent but 1-2 members feel off, OR the cluster reads like a finer subdivision of the nearest prior. Accept, but reuse the nearest prior label rather than minting a new one.
- **FAIL**: the members' actions are orthogonal (= different workflows that happen to share vocabulary). Example: one member is about ordering/sequence, another about frequency/intensity — same words, different idea. Reject.

When you PASS, supply:
- `label`: a fresh kebab-case slug naming the skill (lowercase ASCII letters/digits/hyphen). It becomes the SKILL.md name + the ~/.claude/skills symlink. Make it specific and durable — it is sacred once published and cannot be renamed without breaking the skill chain.
- `description`: a one-line skill auto-trigger description starting with "Apply when ..." or "Use when ...". This is what Claude Code matches on to surface the skill, so it must capture the precise condition the workflow applies under.

When you DOUBT, set `label` to the nearest prior label ("{{.NearestPriorLabel}}") and `reuse_prior_label` to true.

When you FAIL, `label` / `description` may be empty.

Always supply:
- `verdict`: PASS | DOUBT | FAIL
- `confidence`: 0.0 - 1.0, your certainty in the verdict
- `reason`: one or two sentences explaining the decision. For DOUBT / FAIL this is the audit trail the operator reads to understand why a cluster did not become a fresh skill.

Return a JSON object matching this schema:

```json
{
  "type": "object",
  "required": ["verdict", "confidence", "reason"],
  "properties": {
    "verdict":           {"type": "string", "enum": ["PASS", "DOUBT", "FAIL"]},
    "confidence":        {"type": "number", "minimum": 0, "maximum": 1},
    "label":             {"type": "string", "pattern": "^[a-z0-9]+(-[a-z0-9]+)*$"},
    "description":       {"type": "string", "maxLength": 200},
    "reason":            {"type": "string", "maxLength": 500},
    "reuse_prior_label": {"type": "boolean"}
  }
}
```
