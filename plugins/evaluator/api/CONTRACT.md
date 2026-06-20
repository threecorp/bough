# SkillEvaluator plugin contract (v0.5 freeze, v0.7+ implementation)

v0.5 freezes the wire and Go contract for
`bough-plugin-evaluator-<name>` plugins WITHOUT shipping a working
implementation. The host's `bough capability evaluate` CLI and the
first official evaluators (GEPA reflective prompt optimiser,
TextGrad gradient evaluator, MUSE-Autoskill lifecycle evaluator,
SkillAudit paired-trajectory auditor) land in v0.7+.

## Why split from CapabilityCompiler

Compilation and evaluation are different concerns:

- A compiler turns approved instincts into candidate artifacts.
- An evaluator decides whether a given artifact should survive,
  be revised, or be pruned.

The bough vision (round 3 synthesis) treats evaluator-driven
evolution (utility scoring, auto-pruning, paired-trajectory
auditing) as the v0.7+ layer that sits above v0.6's compiler
layer. Keeping the interfaces separate means a v0.7 evaluator can
score artifacts produced by any v0.6 compiler without the compiler
exposing an evaluate hook.

## Handshake (reserved for v0.7+)

```
ProtocolVersion: 1
MagicCookieKey:  BOUGH_EVALUATOR_PLUGIN
MagicCookieValue: v1
PluginKey: skill_evaluator
```

## The one RPC

`Evaluate(EvaluateReq) → EvaluateResp`

Score a single CapabilityArtifact and recommend whether the host
should promote, revise, or prune it. Evaluators are stateless from
the host's point of view; if an evaluator needs historical context
(paired-trajectory comparisons, prior confidence trajectory) it
must fetch that from its own backing store, not from bough's
memory.

The artifact flows as opaque JSON bytes (`artifact_json`) rather
than as a typed `CapabilityArtifact` message so v0.7+ evaluator
plugins do not pick up a transitive proto dependency on the
capability surface.

## EvaluationOutcome

| Outcome | Meaning |
|---|---|
| `OUTCOME_PROMOTE` | Promote the artifact (e.g., worktree → repo or repo → global). |
| `OUTCOME_KEEP` | Keep the artifact in its current scope; no action needed. |
| `OUTCOME_REVISE` | The artifact is salvageable but needs a follow-up Compile. |
| `OUTCOME_PRUNE` | The artifact has failed — the coordinator should Forget it. |
