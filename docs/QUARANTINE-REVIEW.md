# Reviewing the policy quarantine

The gate (`internal/instinctgate`) catches minted instincts that teach a
forbidden behaviour. It is a regex, and **a regex cannot tell "do X" from
"never do X"** — so every rule-shaped note trips it, because a rule cannot
be written without naming what it forbids. The same goes for a safety
check that names the command it guards ("before `gh pr merge`, verify the
state"). That is not a tuning problem: it is the guard's known cost, and
the part that decides MEANING is this review.

Nothing here deletes. `retire` leaves the file in quarantine.

## Procedure

**1. List the pending batches.**

```bash
ls -d <homunculus>/projects/<id>/instincts/.policy-quarantine/*/ | while read -r d; do
  [ -f "$d/REVIEWED" ] || echo "pending: $d"
done
```

A batch with a `REVIEWED` marker is done; skip it. The per-prompt
`[bough policy]` notice counts unmarked batches only.

**2. Read every quarantined instinct in full — all of them, not a sample.**

Read the `## Action` **and** the `## Evidence`. Evidence is where a
verdict gets decided: a note can look like ordinary advice until its
evidence names the session where the operator banned the technique.

Do not delegate the verdict to a subagent and accept its answer. Use one
to gather if you like; read the files yourself before deciding.

**3. Decide, per instinct, on one question:**

> Does the Action **instruct** the forbidden behaviour, or **forbid /
> repair / guard** it?

- **instructs it** → `retire`. Say what it teaches and which rule that breaks.
- **forbids, repairs, or guards it** → `keep`. It matched because it names
  what it bans or protects.

Two traps, both real:

- **Negation words prove nothing.** A violation can read "merge as soon as
  CI is green, without waiting for review". A negation heuristic scores it
  compliant. Read the sentence, do not scan it.
- **Mixed instincts exist.** A note that is mostly sound and ends in one
  banned bullet gets retired: the bullet a skill would copy is the banned
  one. Rewriting it is a separate decision (step 5).

**4. Record each verdict — the command writes it, you do not hand-edit
the config.**

```bash
bough instinct verdict keep   <id> --why "<why it is the rule / correct>"
bough instinct verdict retire <id> --why "<what it teaches, and which rule that breaks>"
```

`keep` appends the id to `instinct.gate.allow_ids` in `.bough.yaml` with
the reason as a comment, and restores the file to `.staging` — the next
observer pass re-adopts it, and the gate reports it as
`exempt (reviewed)` from then on. `retire` appends the reason to the
batch `REPORT.md` and leaves the file where it is.

**5. Rewrite only what carries technique found nowhere else.** Most
retirements should stay retired. A rewrite must not name the banned
command — then it needs no `allow_ids` entry and the guard stays tight.

**6. Close the batch and prove it.**

```bash
bough instinct verdict done --batch <dir>
bough instinct observer run-once   # the staged keeps re-screen as exempt
```

## Do not

- **Do not delete.** Not the file, not the batch.
- **Do not add an `allow_ids` entry by hand.** Use `verdict keep`; the
  reason belongs with the decision that produced it.
- **Do not widen a tripwire to dodge a false positive.** The allowlist is
  the escape hatch; a loosened pattern misses silently.
