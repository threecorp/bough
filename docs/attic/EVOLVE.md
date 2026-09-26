# bough instinct evolve pipeline (v0.7.1)

The evolve pipeline turns raw Claude Code hook observations into
state=candidate Instinct rows the operator can review + promote.
v0.7.1 ports the upstream ECC `/evolve-skill-manual-v3` algorithm
into Go and wires it behind `bough bootstrap --apply`.

## Architecture

```
.bough/observations.jsonl  (raw hook events from `bough hook handle`)
        │
        ▼
internal/cli/bootstrap.go (--apply)
        │
        ▼
Pipeline.Run
   ├─ Gate 1 schema validation     (drop malformed)
   ├─ Gate 2 heuristic filter      (drop low-quality / anti-pattern)
   ├─ Gate 3 Jaccard clustering    (group similar observations)
   ├─ LLM judge (GATE 5)           (PASS / DOUBT / FAIL per cluster)
   ├─ Gate 4 candidate stamping    (mint InstinctCandidate rows)
   ▼
bootstrap.Apply
   ├─ git-clean check on .claude/  (--force overrides)
   ├─ atomic tmp+rename per file
   ├─ verdict gate (PASS / DOUBT+force / FAIL)
   ▼
.claude/skills/<label>.md  (operator-reviewable artifact)
```

## The 4 Gates

### Gate 1 — schema validation

Drops `TraceBundle`s missing required fields (`ID`, `Source`,
`Scope.Level`, `Content`, `CapturedAt`) or with a CapturedAt more
than 24 hours in the future (= clock-skew guard).

### Gate 2 — heuristic filter

Rejects observations the LLM judge would also reject, saving an
expensive call. Defaults (all overridable in `internal/evolve`):

| Constant         | Default | Rationale                          |
|------------------|---------|------------------------------------|
| `MinContentLen`  | 24      | Single-word observations never     |
|                  |         | survive the upstream judge.        |
| `MinTokenCount`  | 4       | Stops repetition padding ("yes yes |
|                  |         | yes yes yes") from passing.        |
| `AntiPatterns`   | 8 entries | "todo:" / "fixme:" / "i don't    |
|                  |         | know" etc. — words the judge       |
|                  |         | reliably FAILs anyway.             |

### Gate 3 — clustering

O(N²) Jaccard sweep on token sets. Threshold = 0.4 (= ECC Python
v3 canonical value). Each cluster carries:

- `Members`             list of TraceBundles
- `MemberHashes`        SHA256 of each member's content
- `NearestPriorLabel`   nearest existing instinct (= when supplied)
- `NearestPriorDesc`    description of that prior

`Gate3Cluster` sorts clusters by size descending so the audit log
surfaces the most-evidenced clusters first.

### LLM judge (= GATE 5 in ECC vocabulary)

Sits between Gate 3 and Gate 4. The `JudgeClient` interface lives
at `plugins/capability/api/llm.go` and accepts a `JudgeRequest`,
returns a `JudgeVerdict`:

```go
type JudgeClient interface {
    Name() string
    Judge(ctx context.Context, req JudgeRequest) (JudgeVerdict, error)
}

type VerdictKind string  // PASS / DOUBT / FAIL
```

Three reference implementations ship in `internal/judge/`:

- **`HeuristicJudgeClient`** — deterministic rule-based judge. No
  LLM call. v0.7.1 default. Decision tree pivots on cluster size
  + hash diversity + nearest-prior proximity.
- **`ReplayJudgeClient`** — fixture playback rooted at
  `.evolve/judgements/`. The on-disk format is the audit record
  shape, so any captured judgement can be moved into a replay
  corpus without translation.
- **`ClaudeJudgeClient`** — stub returning `ErrClaudeNotWired` in
  v0.7.1; v0.7.2 lands the Anthropic SDK integration.

Backend selection: `bough bootstrap --apply --judge {heuristic|
replay|claude}`. The wrapper `CachedJudge` reads from the audit dir
on every call so an unchanged (request → verdict) pair never bills
a second time.

### Gate 4 — candidate stamping

Per cluster member, mints one `InstinctCandidate`:

- `State = candidate`
- `Confidence` clamped to ≤ 0.5 when verdict is DOUBT
- `HowToApply` = `verdict.RecommendedLabel || cluster.NearestPriorLabel`
- `DedupeKey` = `sha256(normalize(Rule) | Scope)` — matches the
  contract in `pkg/schema/instinct.go`

FAIL verdicts produce zero candidates (= cluster dropped entirely).

## Cache key composition

```
sha256(
    prompt_version | 0x00 |
    model_id       | 0x00 |
    cluster_member_ids   | 0x1F-joined | 0x00 |
    cluster_member_hashes | 0x1F-joined | 0x00 |
    nearest_prior_label  | 0x00 |
    nearest_prior_description
)
```

Field separators (0x00) bound top-level fields; list separators
(0x1F) bound list items. A member id containing `"|"` therefore
cannot collide with the join character.

## Audit dir layout

```
.evolve/
  judgements/
    <cache_key>.json   # AuditRecord shape (see internal/evolve/audit.go)
  prompts/
    v3-2026-06-23.txt  # prompt template snapshot
```

Every CachedJudge call appends one record. Records are atomic
(tmp+rename) and idempotent (same input → same output → same file).
`bough claude doctor` will surface cumulative cost (= sum of
`cost_estimate_usd` across records) once the ClaudeJudgeClient
lands in v0.7.2.

## Applying candidates

`bough bootstrap --apply` writes PASS candidates into
`.claude/skills/<label>.md` with the following frontmatter:

```markdown
---
name: io-lives-in-data-layer
description: "triplet+ cluster with 3 distinct hashes across 3 members"
generated_by: bough@v0.7.1
generated_at: 2026-06-23T10:00:00Z
verdict: PASS
confidence: 0.80
cluster_size: 3
---

# io-lives-in-data-layer

triplet+ cluster with 3 distinct hashes across 3 members

## Evidence

- I/O lives in data layer; usecase calls wrappers via interface (scope=repo)
...
```

Safety guards:

1. **Refuses if `.claude/` is dirty** in `git status`. `--force`
   overrides.
2. **Atomic write via `<target>.tmp` + `os.Rename`.** A half-
   flushed file never appears in the working tree.
3. **`git diff --stat` summary** printed for operator review.
4. **Verdict gate**: PASS auto-promotes, DOUBT requires `--force`,
   FAIL is always skipped (even with `--force`).

## Quality gates (= post-tool hook)

`.bough.yaml`:

```yaml
quality_gates:
  - name: typecheck
    command: "nix develop -c make test-short"
    on_event: PostToolUse
    on_tool: Edit
    on_match: ".*\\.go$"
    timeout_seconds: 120
  - name: lint
    command: "golangci-lint run --new-from-rev=HEAD~1"
    on_event: PostToolUse
```

v0.7.1 ships the runner + config schema; v0.7.2 wires it into
`bough hook handle`'s PostToolUse dispatch.

## Golden corpus

`internal/evolve/testdata/golden/` pins the pipeline output
against a fixed synthetic input set. Refresh:

    UPDATE_GOLDEN=1 go test -run TestGolden ./internal/evolve/...

v0.7.1 is Go-vs-Go regression only. v0.7.2 lands the Python v3
parity diff via `bough instinct import`.
