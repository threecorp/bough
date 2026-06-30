# Changelog

## v0.9.20

### Changed

- **Evolved skills are now PROJECT-scoped, not global.** `bough evolve --generate` symlinks generated
  skills into the monorepo's `<repo>/.claude/skills/` (and `bough create` symlinks
  `<worktree>/.claude/skills → <repo>/.claude/skills`) instead of the global `~/.claude/skills/`. A
  bough-evolved skill is specific to the repo it was learned from, so global linking polluted every repo
  and let a generic slug from one project silently clobber another's same-named skill. The per-worktree
  symlink is required because `claude --worktree` cd's into the worktree — a non-git container whose git
  walk-up cannot reach the monorepo root's `.claude/skills`. The homunculus stays the single source of
  truth (symlinks, no copies). `--no-symlink` still opts out.

### Fixed

- **`bough evolve` reads the monorepo-root instinct corpus.** It resolved identity from the raw cwd, so
  running `bough evolve` from a sub-repo / worktree read a different (usually empty) project than the
  observer / writer pool into. It now uses `resolveMonorepoRoot(cwd)`, consistent with inject / session-end
  / observer.

### Internal

- Dropped `WriteSkill`'s now-dead `symlinkDir` parameter and the `refreshSymlink` helper from
  `internal/evolve`. Project-scope symlinking moved to `cli.ensureSymlink` / `cli.deployProjectSkills`,
  so the in-emitter symlink path was unreachable. `ensureSymlink` now also resolves its target to an
  absolute path so a relative `BOUGH_HOMUNCULUS_DIR` cannot produce a dangling link.

### Notes

- The project `<repo>/.claude/skills/<slug>` entries are symlinks into your per-user homunculus
  (`~/.local/share/bough-homunculus/...`), so their targets are **machine- and user-specific**. If your
  monorepo root is under version control, do not commit them — add `.claude/skills/` to `.gitignore` (or
  keep the bough root out of git, as the auba monorepo does). `bough evolve` regenerates them.

## v0.9.19

Follow-up to v0.9.18: the three items deferred from the #48–#54 review are now fixed
(no carryover).

### Fixed

- **Confidence demotion no longer fires on ordinary task prompts.** The correction-marker
  set drops the task-dominant "error" / "fix" / "correct" (which dominate normal task
  prompts like "fix the bug" / "there's an error"), keeping only words that read as a
  correction of the assistant ("wrong" / "mistake" / "incorrect" / "undo" / "revert" /
  "broke" / "broken"). With the v0.9.18 prompt-scope + whole-word matching, a good instinct
  is now demoted only when the user actually corrected bough.
- **`bough create` no longer aborts on a bad env_local template.** A failed `.env.local`
  render/write is now best-effort (like `post_create`): it is recorded and surfaced in the
  partial-failure summary, but the worktree-path stdout emit (the WorktreeCreate cd
  contract) and `--strict` accounting still happen, so Claude Code still lands in the
  worktree.
- **The observer daemon reaps its `claude --print` grandchild on shutdown.** The run-once
  pass now runs in its own process group (Setpgid) and the daemon kills the whole group on
  cancel, so a `claude --print` spawned by an in-flight pass is no longer orphaned to init
  after `observer stop`.

## v0.9.18

Retrospective /review of the merged continuous-learning PRs #48–#54 (which shipped
without a pre-merge review), batched into one fix release. One reviewer agent per PR;
every finding verified against the live code before fixing.

### Fixed

- **Minted instincts are now injected in monorepo / worktree sessions (HIGH).**
  The observation writer, observer daemon, session-end and preserve all key the project
  on `DetectIdentity(resolveMonorepoRoot(cwd))`, but the two injectors
  (`bough inject-context` and the inline UserPromptSubmit dispatch) keyed on the raw
  `cwd`. In a multi-repo monorepo / worktree (the `.bough.yaml` root is not a git repo;
  each sub-repo has its own origin) those resolve to different project ids, so the
  injector read an empty project and surfaced nothing — the observe→mint→inject loop's
  payoff was silently dead for the primary topology. Both injectors now resolve the
  monorepo root.
- **Confidence demotion no longer fires on tool-output noise (HIGH).**
  The correction signal scanned tool OUTPUT with a naked substring match, so `"0 errors"`,
  `"prefix"`/`"fixtures"`, `"correctly"` etc. — present in essentially every session —
  demoted good instincts every session, the exact opposite of the intent. It now scans the
  USER's prompts only, as whole words. (Residual, documented: a marker can still sit in an
  opening task prompt; a precise correction-of-assistant signal is a future refinement.)
- **Observations with a secret-keyed unquoted scalar are no longer dropped (HIGH).**
  Redacting raw JSON bytes turned `{"token":12345678}` into `{"token":[REDACTED]}` —
  invalid JSON — which `json.Marshal` then rejected, silently discarding the whole
  observation. `sanitizeObservation` now guarantees it never turns a valid payload into
  invalid JSON (falls back to truncation alone), so a record is never lost.
- **Secrets inside escaped-JSON string values are now redacted (security).**
  The redaction separator class gained a backslash, so a credential in a tool's JSON-body
  stdout (`{"stdout":"{\"access_token\":\"…\"}"}`) is caught instead of leaking.
- **`bough observer run-once` resolves the monorepo root** (was raw cwd), so it reads the
  same archive the hook/daemon pool into; the stale `.bough/observations.jsonl` inbox is
  documented as a legacy back-compat read, not a live path.
- **`bough observer stop` no longer risks killing an unrelated process on SIGKILL
  escalation.** The escalation path re-verifies the pid is still the observer daemon
  before SIGKILL (the recycled-pid class closed for the SIGTERM target in v0.9.17, closed
  here for escalation too), and the post-kill wait result is reported.
- **`bough doctor` reports observer capture from the homunculus** observations.jsonl
  (resolved read-only) instead of probing the dead working-tree `.bough/observations.jsonl`
  — it no longer says "not yet capturing" while the loop is healthy.
- **`bough instinct promote` previews by default.** It mutates the shared global corpus, so
  a bare `promote` now reports what it would do; pass `--apply` (or `--dry-run=false`) to
  write — matching `bough ecc import`.
- **`bough create` returns a trimmed `branch_strategy`** so a quoted `"  develop  "` no
  longer reaches `git worktree add -b` as an invalid ref.
- **`session-evolve-claudemd --write` clears a stale proposals file** when nothing crosses
  the gates, so operators do not review proposals that no longer hold.
- **`resolveMonorepoRoot` verifies the `.bough.yaml` marker** before trusting a
  `/.worktrees/` split, so a path that merely contains that segment does not resolve to a
  bogus root.
- **Docs corrected:** a PreCompact hook's plain stdout does not reach the post-compaction
  model context (only SessionStart/Setup do); the durable MEMORY.md snapshot plus the
  next UserPromptSubmit inject are what re-surface instincts. Comments overstating a
  "fold into the compacted context" were fixed.

## v0.9.17

Retrospective /review of merged PRs #44 / #46 / #47 (which shipped without a pre-merge review).

### Fixed

- **`bough observer stop` no longer risks killing an unrelated process (recycled-pid kill).**
  `daemonRunning` trusted the pid-file value on a bare signal-0 liveness check; a crash/SIGKILL leaves
  a stale pid file, and if the OS recycled that pid, `stop` would SIGTERM/SIGKILL whatever now holds it
  (e.g. the operator's editor). It now verifies the pid's command line is an
  `observer _run-daemon --root <root>` before trusting it (shared `daemonLineMatches`).
- **Observer stop/status resolve the monorepo root consistently** (`resolveObserverProject` now
  canonicalizes via the monorepo root), so a daemon started from the repo root is still found when
  stop/status run from a sub-dir / worktree — the lookup-key asymmetry no longer orphans daemons.
- **Prompt-preview truncation is rune-safe.** `truncatePreview` / `previewRawJSON` cut on a rune
  boundary, not bytes, so a Japanese (3 bytes/char) field over the cap is no longer sliced into a
  U+FFFD mojibake char (matches scrub.go).
- **`bough create` fails clearly when a repo has no `branch_strategy` and no detectable default branch**
  (origin/HEAD unset) instead of an opaque `git worktree add -b … ""` exit-128.
- Minor: the daemon's authoritative pid-file self-write ensures the project dir exists first.

## v0.9.16

### Added

- **`repositories[].source` — bough clones a sub-repo it does not have yet.**
  When `<monorepoRoot>/<name>` is absent and a `source:` is declared,
  `bough create` acquires it first: a remote git URL (`git@host:org/repo`,
  `https://…`, `ssh://…`, `file://…`) is cloned over its transport; a local
  path (`~/…`, `./…`, `../…`, `/abs`) is cloned with `--local`
  (hardlink-fast, offline). `name` is optional when `source` is set — it is
  derived from the source basename (`source: git@github.com:org/auba-proto`
  → name `auba-proto`). Clone is best-effort like the rest of the create
  loop (a failure logs + skips that repo; `--strict` turns the run
  non-zero). This lets a `.bough.yaml` be self-contained — no hand-cloning
  the sub-repos before the first `bough create`.

### Changed

- **`repositories[].branch_strategy` is now optional.** When omitted, bough
  uses the repo's default branch (origin/HEAD); an explicit value still wins
  over origin/HEAD (the v0.9.15 fix). Previously it was required. Each
  repository must still declare `name` **or** `source`.

## v0.9.15

### Fixed

- **`bough create` now honors the declared `branch_strategy` over a clone's
  `origin/HEAD` when choosing a worktree's base branch.** Previously
  `branch_strategy` was passed as `DetectBase`'s *fallback*, and
  `DetectBase` returns `origin/HEAD` first — so a `git clone --local` whose
  `origin/HEAD` mirrored the source's checked-out feature branch silently
  cut the new worktree off that feature branch instead of the declared
  base. Found via dogfood: a monorepo declaring `branch_strategy: develop`
  got one sub-repo's worktree based on `feature/issue-1394-…` (because the
  source happened to be on that branch at `--local` clone time) while a
  sibling sub-repo on `develop` was unaffected — exposing the
  inconsistency. The explicit, required `branch_strategy` is now
  authoritative; `origin/HEAD` auto-detection is used only when
  `branch_strategy` is empty (`chooseBase` in `internal/cli/create.go`).

## v0.9.14

### Added

- **Opt-in: auto-run CLAUDE.md proposals on SessionEnd
  (`instinct.evolve_claudemd_on_session_end`).** When set to `true` in
  `.bough.yaml`, the SessionEnd hook now also runs `session-evolve-claudemd`
  in write mode, so `<monorepoRoot>/.claude/claudemd-proposals.md` is
  regenerated every session — matching threecorp ECC's automatic
  `evolve-claudemd.sh`. Default is `false`: this is the one hook action that
  writes into the repo working tree (every other bough hook action writes
  only to the homunculus), so the no-contamination default is preserved
  unless the operator explicitly opts in. Pure filesystem; no LLM, no
  billing. The config is read from the resolved monorepo root, so a sub-repo
  / worktree session still finds it.

### Fixed

- Corrected stale `bough hook handle` help / Short text that still named
  `.bough/observations.jsonl` as the default observation path — since
  v0.9.10 the default is the central homunculus `observations.jsonl`.

## v0.9.13

### Added

- **`bough instinct promote` — project → global instinct promotion (ECC parity, #5).**
  Scans every registered project's instincts and copies the ids that
  independently reached >= 2 projects with mean confidence >= 0.8 into the
  global corpus (`~/.local/share/bough-homunculus/instincts/personal`),
  which `inject` layers into every project — closing the loop that read the
  global corpus but never wrote it. Faithful port of ECC
  continuous-learning-v2's auto-promotion (`PROMOTE_MIN_PROJECTS=2`,
  `PROMOTE_CONFIDENCE_THRESHOLD=0.8`, arithmetic-mean confidence,
  highest-confidence body on conflict, idempotent skip of already-global
  ids, source instincts left untouched). bough adds `.md` storage (so
  `inject` reads it), provenance frontmatter (`source` / `promoted_date` /
  `seen_in_projects` / `promoted_from`), `--dry-run`, and
  `--min-projects` / `--min-confidence` overrides.
- **`bough session-evolve-claudemd` — CLAUDE.md evolution proposals (ECC parity, #10).**
  Scans the project's instincts and proposes the high-confidence ones as
  CLAUDE.md additions + the decayed, aged ones as removals, for human
  review. Mirrors ECC's `evolve-claudemd.sh`, which — contrary to the
  earlier design note — is **pure filesystem, not an LLM call**: the
  instinct trigger/body already reads as a candidate rule, so neither ECC
  nor bough needs a model to phrase it. No `claude --print`, no billing,
  and CLAUDE.md is never edited automatically. Preview to stdout by
  default; `--write` saves `.claude/claudemd-proposals.md`. Confidence
  gates are calibrated to bough's 0.30-0.85 ladder (ECC's 0.9 ADD gate
  would be inert against the 0.85 cap): ADD at >= 0.80 (top two bands),
  REMOVE at <= 0.30 aged > 30 days.

### Security

- **Observations are secret-scrubbed + per-field-truncated at write (ECC parity, #7).**
  `hook handle` now redacts secret tokens (verbatim port of ECC
  observe.sh's `api_key|token|secret|password|authorization|credentials|auth`
  regex) and caps each string field at 5000 chars before appending to
  `observations.jsonl`. The scrub runs at the string level, so secrets
  embedded in command values (e.g. `export API_KEY=...`) are caught, not
  just JSON keys; only the persisted copy is sanitized — quality-gate
  matching still sees the real command. Defense-in-depth (the homunculus
  already lives outside any repo) plus smaller rendered prompts.

### Removed

- Dropped the unused `session_evolve_claudemd` prompt template stub — #10
  is pure-filesystem, so no LLM prompt is needed.

## v0.9.12

### Changed

- **Instinct confidence can now DECREASE on a correction (ECC parity, #4).**
  `session-end` previously only ever reinforced (token overlap → +1 band),
  so a wrong instinct never decayed. It now detects ECC's correction
  markers (`error|mistake|wrong|fix|correct|undo`) in the session's tool
  outputs + prompts and DEMOTES the instincts the session exercised (−1
  band) instead of reinforcing them. The band ladder extends down to
  0.30 / 0.40 (below inject's 0.50 floor) so a repeatedly-contradicted
  instinct decays out of the injected set — bough's analogue of ECC's
  demotion-toward-removal. Targeting stays per-instinct (token overlap),
  which is more precise than ECC's session-global hurt/helped flag.
- **GATE 5 evolve judge defaults to a Sonnet-class model (#8).** The
  high-frequency observer extraction stays on haiku (matching ECC's Haiku
  observer), but the low-frequency, high-stakes skill accept/reject now
  defaults to `sonnet` (ECC runs its semantic work on Sonnet). `--model`
  still overrides. bough evolve already mirrors `/evolve-skill-manual-v3`.
- **Observations are archived past 10 MiB (#9).** `hook handle` rotates a
  project's `observations.jsonl` into `observations.archive/` once it
  exceeds 10 MiB (ECC observe.sh's threshold), so the live file the
  observer tails stays bounded instead of growing without limit.

## v0.9.11

### Changed

- **`bough hook handle` fans out to the per-event ECC actions, so the
  observe → evaluate → preserve loop closes on the default install (ECC
  parity).** Previously every event routed to the same handler that only
  appended an observation; the `session-end` / `preserve-instincts`
  handlers existed but were orphaned (never wired). `hook handle` now
  dispatches internally: UserPromptSubmit → inject the instinct block
  (unchanged), **SessionEnd → evaluate instinct confidence**, **PreCompact
  → preserve top instincts**. bough keeps its single wired command
  (simpler / more testable than ECC's N-script settings.json fan-out),
  and LLM extraction stays opt-in via the observer daemon (no surprise
  `claude --print` on session close). SessionEnd / PreCompact resolve the
  project via the same monorepo-root logic the hook writes with, so they
  read the observations the hook actually wrote.
- **PreCompact preserve now prints the top instincts to stdout** (in
  addition to the durable `MEMORY.md`) so they fold back into Claude
  Code's compacted context — the load-bearing ECC behavior bough was
  missing.

## v0.9.10

### Changed

- **Observations now live in the central homunculus, never the repo
  working tree (ECC parity).** `bough hook handle` previously wrote
  `<cwd>/.bough/observations.jsonl` relative to the session cwd, so a
  `.bough/` sprouted in every repo / sub-repo / worktree a Claude Code
  session ran in, and observations fragmented across dozens of tiny
  files too sparse to cluster. It now resolves the monorepo root — a
  session inside `.worktrees/` maps to its parent; otherwise walk up to
  the nearest `.bough.yaml` — and appends to that project's homunculus
  observations file (`~/.local/share/bough-homunculus/projects/<id>/`),
  exactly like upstream ECC's `observe.sh` + `observe-wrapper.sh` (which
  rewrite the hook cwd to the monorepo root so every sub-repo pools into
  one project). Result: zero working-tree pollution, and all sub-repos /
  worktrees pool into one rich per-monorepo corpus. An explicit `--out`
  still overrides (replay / conformance), and capture is best-effort —
  a write failure never fails the operator's tool call. Verified by
  dogfooding: a `hook handle` fired from a sub-repo wrote the monorepo's
  homunculus file and created no `<sub-repo>/.bough/`.

## v0.9.9

### Fixed

- **The observer daemon survived `SIGTERM`, so `observer stop` could not
  actually stop it (completes #45).** `cmd/bough/main.go` installs
  `signal.NotifyContext` for SIGTERM/SIGINT, which makes those signals
  *cancel the root context* rather than terminate the process — but the
  `_run-daemon` loop ran a bare `for { … time.Sleep }` that never watched
  `ctx.Done()`, so a SIGTERM'd daemon kept ticking and only SIGKILL could
  end it (exactly why the v0.9.8 stale-pid incident's leftover daemon
  needed a manual SIGKILL). The loop now `select`s on `ctx.Done()` and
  exits cleanly (dropping its pid file); `runObserverOnceQuiet` uses
  `CommandContext` so a mid-pass SIGTERM interrupts the `claude --print`
  call; and `observer stop` escalates to SIGKILL if the process is still
  alive after a 3s grace, so it never reports success on a still-live
  daemon. Verified by dogfooding: `observer stop` against a daemon with a
  deliberately-corrupted pid file now finds it (the v0.9.8 fallback) AND
  the daemon dies "via SIGTERM".

## v0.9.8

### Fixed

- **`observer stop` / `status` could miss a live daemon and report it
  "not running" (#45).** Both trusted `<project>/observer.pid`
  exclusively; if that file was stale or held the wrong pid (a spawn
  race, or a daemon launched out-of-band), `stop` signalled nothing and
  orphaned a daemon that kept ticking against a project the operator
  believed was idle. The `_run-daemon` loop now writes its OWN pid on
  startup (the authoritative record), and `stop` / `status` fall back to
  discovering a live `observer _run-daemon --root <root>` process by
  command line when the pid file is stale or missing. Found by
  dogfooding: a leftover daemon (pid 49920) survived `observer stop`
  because the pid file held a dead pid (85654).

### Changed

- **`bough create` surfaces post-setup failures loudly and gains
  `--strict`.** A failed `git worktree add` for one repo, or a failed
  `post_create` hook (e.g. a migration), was logged mid-stream but
  create still exited 0 — so a WorktreeCreate hook looked fully
  successful when the environment was half-built. create now prints a
  prominent final WARNING summarising every worktree-add / post_create
  failure (the worktree path is still emitted so Claude Code can cd in),
  and `--strict` turns any such failure into a non-zero exit for CI /
  scripted callers. The default stays best-effort (exit 0 once the
  worktree exists) to preserve the hook's cd contract.

## v0.9.7

### Fixed

- **`bough create` died with `exit 128` when a sub-repo's default branch
  name contained a slash (`feature/…`, `release/…`).** `gitwt.DetectBase`
  resolved the base branch from `origin/HEAD` by splitting on the *last*
  slash of `refs/remotes/origin/<branch>`, so
  `refs/remotes/origin/feature/x` became `x` — an invalid ref — and the
  subsequent `git worktree add … x` failed with
  `fatal: invalid reference: x`. DetectBase now strips the
  `refs/remotes/<remote>/` prefix and keeps the full branch path
  (`feature/x`). A repo whose `origin/HEAD` points at `develop` was
  unaffected, which is why this surfaced only on a sub-repo whose origin
  default was a feature branch. Earlier "engine-ordering" and
  "argument-order" hypotheses were both wrong; the root cause was proved
  by replaying git directly (mangled base → 128, full branch → 0). (#41)
- **`gitwt` worktree-add failures now surface git's actual message.** The
  add / attach calls used `.Run()`, which discarded stderr, so the
  failure above reached the operator as a bare "exit status 128". They
  now use `CombinedOutput()` and fold git's `fatal: …` line into the
  wrapped error.

### Changed

- **README install: macOS (Apple Silicon) re-sign note.** The release
  binaries are not notarized, so Gatekeeper kills them on first run
  ("zsh: killed"). The Install section now documents the one-time
  `codesign --force --sign -` (+ `xattr -dr com.apple.quarantine`)
  step. (#42)

## v0.9.6

### Fixed

- **CRITICAL: the live observe → mint loop rendered hollow prompts —
  every extraction pass minted zero instincts.** `bough hook handle`
  stores each Claude Code event verbatim under an envelope
  `{"ts","event","payload":{…}}`, with the tool name / input / cwd /
  session id nested inside `payload` under Claude Code's own field names
  (`tool_name`, `tool_response`). But the observer's `Observation`
  struct only declared the *flat* schema (`tool`, `tool_input`, `cwd`
  at top level) that `observe.Writer.Append` / `bough ecc import` emit,
  so unmarshalling a hook-captured record bound only `ts` + `event` and
  dropped everything else. `buildObserverData` then re-marshalled those
  hollow records into the prompt, so Claude received a window of
  `{"ts","event"}` lines with no substance and correctly minted
  nothing. Same class as the v0.9.3 / v0.9.5 "loop inert" bugs (a reader
  that never matched the writer's real shape), and the last blocker for
  the observe → mint half of the loop on any live-hook corpus — masked
  because the only corpus that ever minted (`bough ecc import`) writes
  the flat schema directly. `Observation.UnmarshalJSON` now reads
  **both** shapes: the nested Claude Code payload backfills whatever the
  flat layer leaves empty (`tool_name` → Tool, `tool_response` →
  ToolOutput, plus cwd / session_id and a new `prompt` field for
  UserPromptSubmit). Found by dogfooding the loop on a real 630-record
  hook corpus: every record rendered as `{ts,event}` and the pass
  emitted 0; after the fix the same corpus minted a correct instinct.

### Changed

- **The observation slice sent to Claude is now field-capped.** Making
  the hook corpus readable also made every record carry its full
  `tool_input` / `tool_response`; a 200-record window of raw shell
  output ballooned the rendered prompt ~25× (16 KB → 400 KB).
  `renderObservationLine` keeps the identity fields whole (ts / event /
  tool / cwd / session_id) and previews each free-form field to
  `maxRenderedFieldBytes` (512), holding the same window to ~145 KB so a
  fuller window cannot blow Haiku's context and silently regress the
  loop back to zero instincts.

## v0.9.5

### Fixed

- **CRITICAL: the live observe → mint loop was disconnected.** The
  PostToolUse hook appends observations to `<root>/.bough/observations.jsonl`
  (the inbox), but `observer run-once` read only the homunculus archive
  (`~/.local/share/bough-homunculus/projects/<id>/observations.jsonl`),
  which the hook never writes. So `observer run-once` always reported "no
  observations" and minted nothing from real hook-captured data — the
  primary accumulation path for any user. It was masked because the only
  corpus that ever minted (`bough ecc import`) writes the homunculus
  archive directly, bypassing the hook. `observer run-once` now reads
  **both** sources via `observe.TailNMerged` (hook inbox + import archive,
  merged, tail-windowed); a missing source is skipped. Found by dogfooding
  a from-scratch monorepo: a real repo had 330 hook-captured observations
  in `.bough/observations.jsonl` that the observer could never see.

## v0.9.4

Dogfooding observability follow-ups to v0.9.3: streaming GATE 5 progress
and surfacing the counts (skipped instincts, orphan project dirs, capped
judgements) that were previously silent.

### Changed

- **`bough evolve --generate` streams GATE 5 progress** instead of going
  silent for minutes. Each verdict is now printed as it lands
  (`[GATE5] 3: grpc-bearer-auth (3 members) → PASS`), and a DOUBT that is
  really the judge being unavailable (rate-limit / parse) is labelled as
  such and tallied so the operator can tell it apart from the model's own
  DOUBT — re-run with a higher `--max-calls` to judge the capped ones.
- **`bough instinct status` reports skipped instinct files.** A count
  lower than the `.md` file count (files ScanInstincts dropped for an
  unreadable body or a filename ≠ frontmatter-id mismatch) now shows a
  `skipped: N` line instead of leaving the operator to wonder where they
  went.
- **`bough ecc import` warns on orphan project dirs.** A `projects/<id>`
  directory present on disk but absent from `projects.json` is now noted
  as skipped rather than ignored silently.

## v0.9.3

The "make the loop actually run" patch. Dogfooding v0.9.2 against a real
1090-instinct corpus surfaced two defects that left the continuous-
learning loop inert end-to-end: the ECC importer migrated zero instincts,
and neither the observer nor the GATE 5 judge could read `claude --print`'s
output. Both are the same class of bug — a mock/fixture that never matched
the real shape — and both are fixed here.

### Fixed

- **Neither the observer nor the GATE 5 judge could read `claude --print`'s
  output — the whole learning loop was inert.** `claude --print
  --output-format json` returns a *result envelope* (a stream-event array,
  or a `{"type":"result","result":"…"}` object), not the model's answer;
  the model's JSON is a ```json-fenced string nested inside `.result`. Both
  consumers parsed the envelope directly:
  - `observer run-once` (`claudecli.Generate` → `parseJSON`) found no
    `instincts` at the envelope's top level, so it minted **zero**
    instincts from a real session — the first stage of the loop produced
    nothing.
  - `evolve --generate` (`ClaudeJudge.Judge`) unmarshalled the envelope
    into the `Verdict` struct, every field stayed zero, `ValidateVerdict`
    failed, and the pipeline mapped the error to DOUBT for **every**
    cluster — GATE 5's PASS/FAIL/DOUBT decision was discarded and skills
    were emitted on the mechanical gates alone.

  New `claudecli.ExtractResultJSON` unwraps the envelope (array → result
  element, or object → `.result`), strips the code fence, and returns the
  bare model JSON; `Generate` and the evolve judge both route through it.
  Verified on a real corpus: `evolve --generate` now yields PASS verdicts
  that mint fresh labels where the broken path produced 31/31 DOUBT. The
  unit tests fed `FakeExec` a bare JSON object, so the real
  `--output-format json` shape was never exercised; `extract_test.go` +
  `TestProvider_Generate_UnwrapsRealEnvelope` now pin the live
  event-array, single-envelope, bare-object, no-fence and no-result cases.
- **`bough ecc import` silently migrated zero instincts from a re-keyed
  ECC corpus.** ECC dedups a re-keyed project by symlinking its
  `instincts/`, `memory/` and `evolved/` dirs at the physical project
  that still holds the files (`projects/<new-id>/instincts ->
  ../<old-id>/instincts`). The importer skipped every symlink, so
  `--apply` reported "1090 instincts" — the count probe reads *through*
  the link — yet copied none, and `bough evolve` / `inject-context`
  then saw an empty corpus. Directory symlinks are now followed and
  materialised as real files; a symlink to a file is copied by value; a
  dangling link is skipped rather than aborting the migration. Found by
  dogfooding against a real 1090-instinct corpus. The import path had
  no test coverage before — `internal/cli/ecc_import_test.go` now pins
  the dedup-symlink, file-symlink, dangling-symlink and
  catalog-exclusion cases.
- **The hooks conformance test polluted the operator's live registry.**
  `conformance/hooks` drove the real binary without
  `BOUGH_HOMUNCULUS_DIR`, so every run upserted a `TestHooks_EndToEnd_*`
  project (rooted at a since-deleted `/tmp/.../001`) into
  `~/.local/share/bough-homunculus/projects.json`. The subprocess env
  now pins the corpus to a tmpdir.

## v0.9.2

The "full loop" release. v0.9.0 shipped the observer (instinct
extraction), v0.9.1 the evolve pipeline (skill/agent/command
generation); v0.9.2 closes the loop — the next session's context is
seeded with what the last session learned, session-end reinforces
the instincts that proved useful, and an existing ECC corpus can be
migrated in.

The `claude --worktree X` → observe → generate → inject-into-next-
session cycle is now complete. Every LLM touch still runs through
`claude --print` inside the operator's subscription.

### Added

- **`bough inject-context`** — the UserPromptSubmit hook handler.
  Prints the confidence-ranked instinct block (project + global,
  ~9.5 KB cap) so Claude Code folds the most-reliable learned
  patterns into the next turn's context. Confidence-sorted (the
  threecorp improvement over ECC's filename-alphabetical order that
  truncated mid-corpus). Pure filesystem — no claude --print on the
  prompt hot path. `bough hook handle --event UserPromptSubmit` now
  both records the observation AND injects, so one hook entry does
  both.
- **`bough session-end`** — the SessionEnd hook handler. Reinforces
  the confidence of instincts the session exercised (signal = token
  overlap with the observation stream), moving them one band up the
  ECC ladder (0.50 / 0.60 / 0.65 / 0.70 / 0.75 / 0.80 / 0.85), and
  appends the evaluation to eval/scores.jsonl. Pure filesystem.
- **`bough preserve-instincts`** — the PreCompact hook handler.
  Snapshots the top-5 confidence instincts to MEMORY.md so a context
  compaction does not lose them. MEMORY.md is a ScanInstincts
  catalog-skip filename so the snapshot is never re-ingested.
- **`bough observer start / stop / status`** — opt-in background
  daemon that runs `observer run-once` every --interval seconds
  (default 600). PID-file lifecycle under the project's homunculus
  dir; Setsid detach (no systemd / launchd). Nothing auto-starts it.
- **`bough ecc import`** — migrates an existing affaan-m/
  everything-claude-code corpus into bough's separate namespace.
  Default --dry-run reports per-project counts; --apply copies. The
  ECC corpus is never modified.

### Notes

- v0.9.2 completes the v0.9 ECC port. The continuous-learning loop
  (`claude --worktree` → observe → evolve → inject) is end-to-end.
- All hook handlers no-op cleanly on a non-git directory or empty
  corpus so a wired hook never breaks the operator's session.

## v0.9.1

The "Evolve pipeline" release. v0.9.0 shipped the observer half (=
`claude --print` extracts instincts from session observations);
v0.9.1 ships the evolve half — the ECC v3 five-gate clustering
pipeline that turns the accumulated instinct corpus into Claude
Code skills / agents / commands.

All GATE 5 LLM work runs through `claude --print` inside the
operator's subscription. GATE 1-4 are pure-Go mechanical filters
that run with no LLM call, so `bough evolve` (preview) costs
nothing; only `bough evolve --generate` spends subscription tokens
— one `claude --print` call per gate-passing cluster, hard-capped
by the v0.9.0 self-DoS limiter.

### Added

- **`bough evolve`** (preview) / **`bough evolve --generate`** —
  the ECC `/evolve-skill-manual-v3` UX. Preview runs GATE 1-4
  mechanically and lists the gate-passing clusters + the exact
  number of `claude --print` calls `--generate` would make.
  `--generate` runs GATE 5 + writes the artifacts.
- **`internal/evolve/`** — the pipeline:
  - tokenize.go — Tokenize / Jaccard / coverage with the ECC
    stopword list (function words + high-frequency tooling verbs).
  - cluster.go — Discover: weak-attachment seed filter +
    connected-component clustering over the cohesion graph.
  - gates.go — the four mechanical gates, ECC v3 verbatim
    thresholds: MEMBER_MIN=2, COH_MIN=0.20,
    LEXICON_COVERAGE_MAX=0.55, REL_ISOLATION_MIN=0.40. Short-
    circuits at the first failure so a 1k-instinct pass stays cheap.
  - judge.go / judge_claude.go — GATE 5. Verdict {PASS / DOUBT /
    FAIL}. PASS mints a fresh label, DOUBT reuses the nearest prior
    label, FAIL rejects. The evolve package declares RenderFunc +
    GenerateFunc so it has no import edge to claudecli and stays
    unit-testable.
  - labels.go — cluster-labels.json atomic R/W with the "sacred
    string" rule (= published labels are never renamed) + backup-
    before-edit.
  - emit.go — SKILL.md (+ ~/.claude/skills symlink) / agent
    (cluster >= 3 + avg conf >= 0.75) / command (workflow domain +
    conf >= 0.70) renderers + atomic writers. Refuses to clobber a
    non-symlink at the skill link path.
  - pipeline.go — Run orchestrates discovery → gates → judge →
    eligibility into one Outcome the CLI persists (or previews).
- **`prompts/defaults/evolve_judge.md`** — the GATE 5 prompt.
  Renders the cluster members + the four gate metrics so the LLM
  sees the same numbers the mechanical gates did; returns
  structured JSON.
- **`claudecli.Provider.GenerateRaw`** — spawn against an already-
  rendered prompt body, skipping the template pass Generate runs.
  The evolve judge uses this because re-rendering would choke on a
  literal `{{` an instinct body might contain.

### Deferred to v0.9.2

- `bough inject-context` UserPromptSubmit hook (9.5KB cap +
  confidence-sorted LRU), SessionEnd handlers (summary / evaluate /
  evolve-claudemd), PreCompact, optional observer daemon,
  `bough ecc import` migration.

## v0.9.0

The "ECC verbatim port" release. v0.5-v0.8 accreted memory backends,
capability compilers, MCP server, evaluator adapters, judges, evolve
gates of my own design, and ECC import helpers — none of which the
operator's vision needs. v0.9 resets to the threecorp ECC reference
architecture (= upstream affaan-m/everything-claude-code) and ports
it verbatim into Go so the LLM cost stays inside the operator's
existing Claude Code subscription. No Anthropic API call, no
separate billing, no API key.

The user constraint that drove the reset:

> "threecorp ECC はギリギリサブスクリプション内に収まる範囲で hook から
>  LLM を呼び出している。 API 等々使うとサブスクリプション以外の課金が
>  されるので、 そういった実装はするな。"

External-AI review (= two independent passes, 2026-06-23) pinned
Option A′ as the canonical mechanism: `claude --print --output-format
json --json-schema` as a subprocess, with Go validating the
structured output and writing the instinct files itself (= ECC
delegates the Write tool, bough does not, for testability + path-
traversal safety).

### Removed (= no compatibility shim)

The v0.5-v0.8 surface was deleted wholesale on the v0.9-ecc-port
reset commit:

- `internal/{judge,evolve,bootstrap,evaluators,ecc}/`
- `internal/{mcp,capability,export,instinct}/`
- `cmd/{bough-mcp-server,bough-plugin-memory-sqlite,bough-plugin-memory-mem0}/`
- `plugins/{memory,capability,db,evaluator,instinct}/`
- `conformance/{capability,mcp,memory,instinct}/`
- `pkg/schema/{capability,evidence,instinct,stability,budget}.go`
- `examples/memory-plugin-*`

If you depend on any of these surfaces, stay on v0.8.1.

### Added

- **`internal/homunculus/`** — the on-disk corpus shape under
  `~/.local/share/bough-homunculus/` (override via
  `BOUGH_HOMUNCULUS_DIR` or `XDG_DATA_HOME`). The namespace is
  intentionally separate from `~/.local/share/ecc-homunculus/` so
  both systems can coexist. project_id = sha256 first 12 hex of
  the git remote URL (credentials stripped) or the project root
  path. RegistryRW.WriteUpsert is atomic + preserves CreatedAt;
  ReadInstinctFile enforces the filename ↔ frontmatter id match
  that ECC v3.2 silently violated.
- **`internal/observe/`** — `observations.jsonl` writer with
  O_APPEND atomic per-line writes (= multi-goroutine + multi-
  process safe). `SanitizeAnthropicEnv` strips
  ANTHROPIC_API_KEY / AUTH_TOKEN / BASE_URL / Bedrock+Vertex
  twins / CLAUDE_API_KEY from any env slice; the same scrub
  applies to spawned subprocesses and the `bough doctor` env
  warning.
- **`internal/prompts/`** — //go:embed defaults + 3-layer
  override resolver (`<repo>/.bough/prompts/` >
  `~/.config/bough/prompts/` > embedded). Template.Version is
  sha256[:12] of the body so the v0.9.1 GATE 5 cache never mixes
  overridden and embedded prompts. v0.9.0 only ships the
  `observer.md` body; stubs hold the slot for v0.9.1's evolve_*
  templates and the v0.9.2 session-end handler.
- **`internal/provider/claudecli/`** — Option A′ provider. Spawns
  `claude -p <prompt> --model haiku --max-turns 4 --output-format
  json --permission-mode bypassPermissions` with a sanitised env.
  Retries once on transient failure (= empty stdout / timeout /
  connection reset / 429); never retries schema violations.
  Limiter enforces self-DoS: 10 calls/session, 30 calls/hour,
  3 consecutive failures → 15min cooldown. FakeExec hook lets
  unit tests run without a real claude binary.
- **`bough observer run-once`** — synchronous single-shot
  extraction pass. Resolves project_id from $PWD's git remote,
  ensures the per-project homunculus subtree exists, reads the
  tail of observations.jsonl, renders the observer prompt with
  project + session + window data, calls `claude --print`,
  validates the structured JSON, and atomically writes each
  accepted instinct under `<pid>/instincts/personal/`. `--dry-run`
  prints / saves the rendered prompt without spawning Claude.
- **`bough instinct status / list / show`** — read-side counterpart.
  status renders project header + 5-bucket confidence histogram +
  top-3 most recent. list is filterable + sortable. show prints a
  single instinct file verbatim.
- **`bough doctor` — continuous-learning posture block.** New
  section after the v0.7 hook + observer report: claude CLI
  presence, Anthropic env scrub (warns when ANTHROPIC_API_KEY
  etc. are exported in the operator's shell), Self-DoS limiter
  defaults, homunculus root path.

### Pinned constants

- 5-gate evolve thresholds (= v0.9.1 lands the pipeline using
  these): MEMBER_MIN=2 / COH_MIN=0.20 / LEXICON_COVERAGE_MAX=0.55
  / REL_ISOLATION_MIN=0.40 (ECC v3 verbatim).
- Self-DoS: 10 calls/session, 30/hour, 3-failure breaker, 15min
  cooldown.
- Namespace: `~/.local/share/bough-homunculus/`.

### Deferred to v0.9.1 / v0.9.2

- 5-gate evolve pipeline + GATE 5 LLM judge + cluster-labels.json
  + SKILL.md / agent / command emit (= P9.1 v0.9.1 sprint).
- `bough inject-context` UserPromptSubmit hook (9.5KB cap + LRU)
  + SessionEnd handlers (summary / evaluate / evolve-claudemd) +
  PreCompact + observer daemon mode + `bough ecc import` migration
  (= P9.2 v0.9.2 sprint).

External-AI consult handover:
`~/.claude/notes/bough-instinct-generation-handover-2026-06-23.md`
Plan: `~/.claude/plans/bough-v09-claudecli-port.md`

## v0.8.0

The "Evaluator adapters + global hook scope" release. v0.8 bundles
two roadmap items the original plan called for separately (= P5 +
P6 in the Imminent / Long-term lanes): in-process implementations
of the four evaluator strategies the v0.5 SkillEvaluator interface
has been carrying, plus `--scope=user` on the `bough hook` family
so an operator can wire bough's observer once at the user level
rather than per-monorepo.

The memory-backend Store loop (= P4 in the roadmap) stays
deferred per the operator's "memory backend は後回し" direction.
v0.8 ships the read-side of every pipeline that needs it (evolve,
ECC, evaluators); the persistent write surface lands when Letta +
mem0 reconcile are ready.

### Added

- **`internal/evaluators/`** — in-process SkillEvaluator
  implementations behind the v0.5 `plugins/evaluator/api`
  contract:
  - `gepa` — reflective prompt optimiser. Flags scope creep + over-
    generalisation via token count + vagueness markers +
    Constraints/Inputs absence. Recommends revise when any signal
    fires.
  - `textgrad` — gradient evaluator. Diffs the current artifact's
    token set against a prior version (passed in via
    `EvaluatorContextJSON`) and routes by Jaccard distance:
    > 0.7 → promote, 0.3-0.7 → keep, < 0.3 → revise.
  - `muse` — autoskill lifecycle. Uses hit count + regression count
    + last-used timestamp from the audit log. Prunes stale
    (90+ days, no hits) and broken (regression ≥ 3) artifacts;
    promotes high-use (≥ 10 hits, 0 regressions).
  - `skillaudit` — paired-trajectory auditor. Computes token
    overlap against a peer artifact; for high-overlap pairs
    routes by confidence delta (≥ 0.05 → keep current, ≤ -0.05 →
    prune, |delta| < 0.05 → revise both).
- **`bough hook install / uninstall / list --scope=project|user`** —
  global scope reaches `~/.claude/settings.json`. Project scope
  (= default) keeps the v0.7.0 behaviour.
- **`claudeSettingsPath(HookScope)`** helper in
  `internal/cli/hook.go` for downstream commands needing the same
  scope-aware resolution.

### Changed

- `bough-mcp-server` Version reports `v0.8.0`.
- `bough-plugin-memory-mem0` Version reports `v0.8.0`.

### Deferred to dogfooding

- The 12-language rule pack the v0.9 plan referenced is now an
  operator deliverable — feed each language's idioms into
  `bough ecc import` (= v0.7.2) and let the evaluator adapters
  shape the surviving set. The bough OSS stays language-neutral.
- Evaluator-driven retirement loop (= P11 in the roadmap) wires
  in v0.9 once the memory-backend Store path exists to act on
  prune verdicts.

## v0.7.2

The "ECC compat + dogfooding bridge" release. v0.7.0 shipped the
safety floor, v0.7.1 the evolve + LLM judge layer. v0.7.2 lets
bough read the existing upstream affaan-m/everything-claude-code
("ECC") homunculus corpus so an operator with 300+ pre-existing
instincts / skills / agents / commands can pipe them into bough's
schemas without re-running the evolve pipeline.

Quality-gate dispatch (= the wire-up deferred from v0.7.1 P1.5)
also lands here: when `.bough.yaml` declares a `quality_gates:`
section, `bough hook handle` now runs the matching gates after
appending the observation and surfaces per-gate pass/fail to
stderr so Claude Code's next turn sees the result.

### Added

- **`bough ecc import`** — walks the ECC corpus (default:
  `~/.local/share/ecc-homunculus`) and projects each artifact
  family onto bough canonical types:
  - ECC instinct → `schema.InstinctCandidate` (state=candidate)
  - ECC skill   → `schema.CapabilityArtifact` (Kind=skill)
  - ECC agent   → `schema.CapabilityArtifact` (Kind=agent)
  - ECC command → `schema.CapabilityArtifact` (Kind=command)
- **`internal/ecc/`** — parser + discovery package. Parser
  tolerances cover the four shapes the live corpus uses (= sampled
  against the threecorp fork: 1080 instincts / 49 skills / 6
  agents / 116 commands across 4 projects). Frontmatter parsing
  accepts both string ("80%") and float (0.8) confidence values.
- **Quality-gate dispatch** in `bough hook handle` — loads
  `.bough.yaml`, matches each declared gate against the
  (event, tool, file_path, repo) of the incoming hook, and runs
  the matchers through `internal/qualitygate.RunMatching`.
  Per-gate `TimeoutSeconds` caps the runtime (default 60s) so a
  hanging gate cannot block the hook indefinitely.
- **Soft-error reporting** — `bough ecc import` records per-file
  parse errors against the manifest rather than aborting the
  walk. Catalog files (`INSTINCTS.md` / `MEMORY.md` / `README.md`)
  and frontmatter-less personal entries skip silently instead of
  cluttering the soft-errors list.
- **Manifest + JSON outputs** — every dry-run writes
  `.bough/ecc-imports/<ts>/_manifest.md` summarising the projects
  + counts + sample candidates; `--json` adds machine-readable
  `instincts.json` / `skills.json` / `agents.json` /
  `commands.json` siblings.

### Changed

- `bough-mcp-server` Version reports `v0.7.2`.
- `bough-plugin-memory-mem0` Version reports `v0.7.2`.

### Deferred to v0.8

- ECC → memory backend write loop (= P4 memory backend
  integration). v0.7.2 ships the read + project pipeline; the
  Store loop wires once Letta + mem0 reconcile land in v0.8.

## v0.7.1

The "Evolve + LLM judge" release. v0.7.0 shipped the safety floor
(= hook auto-wire, bootstrap dry-run, MCP write hardening). v0.7.1
layers the actual artifact-generation pipeline on top: a 4-gate
mechanical filter ported from the upstream ECC v3 canonical
algorithm, a swappable JudgeClient interface with three reference
backends, SHA256-keyed audit cache, and a `bough bootstrap --apply`
path that writes PASS candidates into `.claude/skills/<label>.md`.

Deviations from the m plan (`~/.claude/plans/bough-v071-sprint-
detail.md`):

* `bough bootstrap` default stays dry-run; `--apply` is opt-in.
  Round 5 reviewers flagged silent CLAUDE.md overwrite as the
  highest-blast-radius failure mode, so v0.7.1 ships actual-run
  as an explicit gesture. v0.7.2 dogfooding can flip the default.
* `ClaudeJudgeClient` ships as a stub returning `ErrClaudeNotWired`.
  Wiring `anthropic-sdk-go` would add a vendor SDK; v0.7.2 picks
  that up alongside the live cost-meter integration `bough doctor`
  surfaces today. v0.7.1 default is `HeuristicJudgeClient` (= LLM-
  free).
* Golden corpus is Go-vs-Go regression only. The Python v3 diff
  defers to v0.7.2 (= `bough ecc import` lands the cross-check
  rig).

### Added

- **`plugins/capability/api/llm.go`** — `JudgeClient` interface,
  `JudgeRequest` / `JudgeVerdict` types, `VerdictKind` enum
  (`PASS` / `DOUBT` / `FAIL`). Lifted into `plugins/capability/api/`
  so v0.8+ can graduate it into a gRPC plugin slot.
- **`internal/judge/heuristic.go`** — deterministic rule-based
  judge over cluster size + hash diversity + nearest-prior
  proximity. v0.7.1 default; works without an API key at zero
  per-call cost.
- **`internal/judge/replay.go`** — fixture playback rooted at
  `.evolve/judgements/`. `ErrReplayMiss` sentinel lets the golden
  corpus harness distinguish cache miss from parse error.
- **`internal/judge/claude.go`** — stub returning
  `ErrClaudeNotWired` until v0.7.2 wires the Anthropic SDK.
- **`internal/evolve/`** — 4-gate Go port of the ECC
  `/evolve-skill-manual-v3` algorithm. Gates split into
  `gate1_schema.go` (drop malformed bundles), `gate2_heuristic.go`
  (length + token diversity + anti-pattern filter), `gate3_cluster.
  go` (Jaccard threshold 0.4 sweep + nearest-prior link), and
  `gate4_candidate.go` (verdict + cluster → `[]InstinctCandidate`).
- **`internal/evolve/cache.go`** — canonical `CacheKey(req)` over
  `sha256(prompt_version | model_id | cluster_member_ids |
  cluster_member_hashes | nearest_prior_label |
  nearest_prior_description)`. Field separators (0x00 / 0x1F)
  prevent collision when ids contain the join char.
- **`internal/evolve/audit.go`** — `AuditDir` + `CachedJudge`
  wrapper. Records persist as
  `.evolve/judgements/<cache_key>.json`; the schema doubles as
  the Replay fixture format. Temperature is overwritten to 0 and
  MaxOutputTokens to 1024 inside `Judge()` so a caller cannot
  bypass the determinism invariant.
- **`internal/evolve/cache.go: ValidateVerdict(v)`** — JSON
  schema validation for `JudgeVerdict`. Invalid live-LLM
  responses fall through to DOUBT instead of poisoning the
  cache.
- **`bough bootstrap --apply`** — runs the 4-gate + judge
  pipeline, atomic-writes PASS candidates into
  `.claude/skills/<label>.md` via tmp+rename, refuses when
  `.claude/` has uncommitted changes (= `--force` overrides),
  and prints a `git diff --stat` summary for operator review.
  FAIL verdicts are always skipped; DOUBT promotes only with
  `--force`.
- **`internal/qualitygate/`** — operator-supplied lint /
  typecheck / smoke runner. Gates declare matchers against
  (event, tool, file path, repo) and run sequentially via
  `sh -c <command>` with per-gate `TimeoutSeconds` cap.
- **`.bough.yaml: quality_gates:`** root section, validated by
  the same LegacyConfig superset migration pattern that v0.6.1
  introduced.
- **`internal/evolve/testdata/golden/`** — Go-vs-Go regression
  corpus. `UPDATE_GOLDEN=1` refreshes expected snapshots when a
  change is intentional.

### Fixed

- **`bough hook replay --fixture -`** now reads from stdin when
  the fixture argument is `-`. k smoke (v0.7.0 post-ship)
  flagged the missing stdin path; the fix is additive.
- **`Makefile: build`** target now also compiles
  `bough-plugin-memory-mem0` and `bough-mcp-server`. The
  v0.7.0 hotfix already shipped on `main` (d25ee97); v0.7.1
  carries it forward.

### Changed

- `bough-mcp-server` Version reports `v0.7.1`.
- `bough-plugin-memory-mem0` Version reports `v0.7.1`.

## v0.7.0

The "Bootstrap safety floor" release. Round 5 external review
(= two independent AI passes on 2026-06-22) split the LLM-touching
surface into v0.7.1 and front-loaded the safety + observability
surfaces into v0.7.0. Every artifact this release generates lands
in a reviewable form (= Markdown proposals under
`.bough/proposals/`) before touching the memory backend; the
operator's `bough instinct approve` is what makes anything active.

No external LLM is called by anything in this release. The MCP
write surface is opt-in (= `--allow-write`); the host wires the
v0.7.0 hardening defaults (worktree-only scope, 60 writes/minute,
append-only audit log) the moment write is enabled.

### Added

- **`bough hook install / uninstall / list / replay / doctor /
  handle`** — the v0.7.0 user-facing surface for the Claude Code
  hook layer. install / uninstall reconcile `.claude/settings.
  json` idempotently and preserve hand-edited entries; list
  renders the current wiring; replay drives a fixture payload
  through the wired handler for debugging; handle is the
  dispatcher Claude Code invokes (writes one JSONL line per
  event to `.bough/observations.jsonl`); doctor is the
  transparency surface (= what is wired, what the observer has
  captured, what the cost meter says).
- **`bough doctor`** — top-level alias for `bough hook doctor`
  so the transparency check is reachable without remembering
  the `hook` namespace. Round 5 reviewer recommendation.
- **`bough bootstrap --dry-run`** — reads
  `.bough/observations.jsonl`, summarises observations per
  event, writes the manifest + per-event Markdown stubs under
  `.bough/proposals/<timestamp>/`. The live (non-dry-run) path
  that mints + persists candidate artifacts via the LLM judge
  lands in v0.7.1. v0.7.0 requires `--dry-run` explicitly so
  operators do not silently get a no-op when they expect a
  write.
- **MCP write hardening** — `Server.SetAuditLogPath`,
  `SetRateLimit`, `SetAllowedScopes`. All optional; zero
  defaults match v0.6.1 behaviour. The host (`bough-mcp-server
  --allow-write`) flips conservative defaults at startup:
  worktree-only scope, 60 writes/minute, audit log at
  `.bough/memory/mcp_audit.jsonl`. Round 5 AI B Q4 mitigation
  set (10 items) fully closed.

### Notes

- v0.6.1 → v0.7.0 is a drop-in upgrade for everyone not using
  the new surfaces. Existing plugins, `.bough.yaml`, and
  `bough-mcp-server` invocations keep working unchanged; the
  `bough hook` / `bough doctor` / `bough bootstrap` commands
  are net-new.
- The v0.7.0 sprint is "safety floor only" — `bough bootstrap`
  without `--dry-run` and the live LLM-judge path land in
  v0.7.1 per `docs/ROADMAP.md`. The non-dry-run invocation
  exits with an explicit "v0.7.0 requires --dry-run" message
  so an operator does not silently get a no-op.
- The new `conformance/hooks/` package runs the install →
  handle → bootstrap → doctor → uninstall chain against a
  built bough binary. Add it to your CI matrix if you build
  bough from source.

## v0.6.1

Drop-in patch on top of v0.6.0 that absorbs three findings from the
2026-06-22 dogfooding session. No schema, plugin contract, or
binary-API breakage — existing plugins and `.bough.yaml` files
upgrade unchanged. The v0.6.1 surface is a strict superset of
v0.6.0 with three additions surfaced as opt-in switches.

### Fixed

- **`bough config validate` accepts the v0.5+ root sections**. The
  internal `LegacyConfig` superset the strict first-pass YAML
  decoder uses had been frozen at the v0.3+v0.4 field set, so any
  `.bough.yaml` that wired `instinct:` / `memory_backends:` /
  `engines:` / `export:` got rejected with `unknown field` even
  though every other subcommand loaded the file cleanly through a
  separate entry point. `LegacyConfig` now mirrors all four
  sections; `migrateLegacy` passes them straight into the canonical
  `Config`. Regression backstop: `TestLoad_acceptsV05Sections`.

### Added

- **`bough-mcp-server --allow-write`** unlocks the two state-
  changing Tools (`memory.store`, `memory.forget`) so MCP clients
  (Claude Desktop, Cursor, Aider) can persist or retire behavioural
  rules from the same stdio surface that already served
  `memory.query`. Off by default to keep v0.6.0 read-only-first
  semantics intact. `memory.promote` stays refused even with the
  flag because it needs the host coordinator (Store(target) +
  Forget(source) pair), which the MCP server intentionally does not
  embed — that lands in v0.7 alongside the Bootstrap layer.

  The server stamps every store with the canonical dedupe key
  (= sha256 of rule + scope, mirroring `internal/instinct.DedupeKey`)
  and forces `state=candidate` so the host coordinator's
  approval flow (`bough instinct approve <id>`) still gates the
  active set. MCP cannot bypass approval. Every store / forget
  writes a one-line stderr audit so an operator running the server
  under `claude --worktree` sees who hit the write surface; the
  coordinator-level `events.jsonl` audit integration follows in
  v0.7.

  Capability advertise mirrors the flag: with `--allow-write`,
  `BoughMCPCapabilities.ReadOnly` flips to false and
  `state_changing_tools` to true so an MCP client can probe the
  writable surface programmatically from `initialize`.

- **`require_signed: true` is enforced at spawn time for memory
  plugins**. v0.6.0 shipped the flag as scaffolding only; v0.6.1
  wires the spawn-time gate `internal/cli.enforceSigning` into
  `discoverMemoryBackend` + `discoverFallbackSQLite` so an
  unverified plugin actually refuses to spawn. Allowlisted plugins
  pass through unchanged; missing verifier tooling (= cosign /
  minisign not on `$PATH`) falls open with a `[NOTICE]` so an
  operator flipping the flag without installing the tools sees what
  is missing rather than tripping over a silent refusal. v0.7 adds
  a `fail_close_on_missing_verifier` knob for enterprise deploys
  and extends the gate to engine plugins. Three env variables wire
  cosign keyless verification:
  `BOUGH_SIGNING_CERT_IDENTITY_REGEXP`,
  `BOUGH_SIGNING_CERT_OIDC_ISSUER`,
  `BOUGH_SIGNING_PUBKEY` (minisign).

### Notes

- v0.6.0 → v0.6.1 is a drop-in upgrade; no migration steps. Existing
  plugins keep working. Operators who flip `require_signed: true`
  should run `bough plugins verify <binary>` against each memory
  plugin in their install path first to confirm the verifier flow
  matches the host's identity / issuer environment variables.
- The MCP server's stderr audit lines start with
  `bough-mcp-server: memory.store:` / `bough-mcp-server: memory.forget:`
  so an operator wiring journald / Loki / CloudWatch ingestion can
  route them deterministically.
- v0.7 plan was reframed during the same dogfooding session. The
  v0.6.x patch series stays focused on tightening v0.6.0 surfaces;
  the Bootstrap layer (= hook-driven auto-generate, `bough init`,
  ECC interop) ships in v0.7 per `docs/ROADMAP.md`.

## v0.6.0

Round 4 external review (June 2026) scoped v0.6.0 to mem0 first-
class + capability compile + read-only MCP + signing scaffolding;
Graphiti is deferred to v0.6.x as a separate GoReleaser archive
(round 4 AI #2).

### Added

- **mem0 official plugin** (`bough-plugin-memory-mem0`): full
  HTTP REST adapter against mem0's v1 API, 30 s TTL LRU 512
  Query cache (Query-only, evicted on Store / Forget / Import),
  namespace mapping that splits user_id (repo identity) +
  session_id (worktree identity) per round 4 AI #2's mem0-layered
  refinement.
- **Read fallback wire** (round 4 AI #1 + #2 split-brain Blocker
  1): when `instinct.fallback_on_error: true` and the primary
  backend is non-SQLite, the host spawns SQLite as a secondary
  process and Coordinator.Query replays primary failures against
  it. Store / Forget / Import never fall back — they fail loud so
  mem0 + SQLite cannot diverge.
- **MemoryBackend.Capabilities widened to 17 fields**
  (round 4 priority A12): adds `temporal_query`,
  `metadata_filter`, `namespace_isolation`, `soft_delete`,
  `bulk_import`, `dedupe_key`, `source_event_id`, `ttl`,
  `eventual_consistency`, `max_batch_size`, `max_query_tokens`.
  Wire is additive; v0.5 plugins continue to advertise only the
  original 5 flags.
- **Graphiti skeleton** (`examples/memory-plugin-graphiti-
  skeleton/`): adapter guide + docker-compose snippet bringing up
  Neo4j 5.13 + getzep/graphiti:latest. The binary lands in v0.6.x
  as a separate GoReleaser archive.
- **CapabilityArtifact + 7 group metadata** (round 4 priority A3
  + round 3 priority B): Target / Invocation / Contract /
  Validation / Provenance groups land alongside the v0.5 fields
  + a sha256 Checksum the CLI uses to short-circuit no-op
  compiles. Wire proto stays at v0.5; the groups round-trip
  through Payload until v0.7's MemoryBackend v2 bump.
- **CapabilityCompiler** orchestrator in `internal/capability/`
  with the registry + dispatch loop. Walks instincts × kinds ×
  targets, stamps Checksum, dispatches through the Emitter
  registry, gathers Artifacts + Emissions.
- **Three builtin emitters** in `internal/export/`: `agent-
  skill` (default — gh skill style markdown + provenance frontmatter),
  `claude-skill` (Anthropic SKILL.md), `mcp` (tool / resource /
  prompt, MCP 2025-11-25). Emitter interface lives in
  `plugins/capability/api/emitter.go` so v0.6.x can graduate
  emitters into plugin slots without rewriting the registry
  (round 4 priority A13).
- **`bough capability compile`** CLI with subcommands compile /
  list / preview / install / lint. `--to <format>` picks an
  emitter; `--profile <host>` selects the runtime layout;
  `--out-dir` persists, otherwise prints to stdout.
- **`bough-mcp-server`** companion binary: stdio JSON-RPC 2.0,
  MCP spec_version 2025-11-25 pinned. Read-only first per round
  4 AI #2 — memory.query is the only tool; state-changing tool
  names refuse with codeWriteForbidden until v0.6.x.
- **Plugin signing scaffolding** (round 4 priority A9 + A10 +
  A11): `InstinctPluginSecurity.AcceptedSignatureSchemes`
  defaults to `["cosign", "minisign"]`. `bough plugins verify
  <binary> [--scheme cosign|minisign]` runs the configured
  verifier; v0.6.0 is fail-open when the verifier tool is missing,
  v0.6.x adds strict mode.
- **Conformance suites** for capability + mcp:
  `conformance/capability/Run(t, cfg)` drives the dispatch loop;
  `conformance/mcp/Run(t, cfg)` walks initialize / tools/list /
  tools/call / resources/list / shutdown across an in-process
  pipe.
- **Documentation**: `docs/CAPABILITY_COMPILER.md`,
  `docs/MCP_SERVER.md`, `docs/SIGNING.md`. EXTERNAL_MEMORY_
  BACKENDS.md gains the mem0 split-brain operational caveat +
  cache + namespace mapping detail.

### Notes

- MemoryBackend wire contract (proto + 7 RPCs) is unchanged from
  v0.5.1; plugin binaries built against v0.5 continue to work.
  The 11 new Capabilities flags default to zero, which v0.6 hosts
  treat as "feature not supported".
- `bough capability compile install` and `lint` are stubs that
  surface a "lands in v0.6.x" message.
- Round 4 priority B / C items (= MEDIUM follow-ups, Evolver
  interface, OpenAI Function Calling emitter, MemoryBackend v2)
  are explicitly deferred — see docs/ROADMAP.md for the timing.

### Post-ship findings (2026-06-22 dogfooding)

These were surfaced after the v0.6.0 tag and Release publish. They
do not invalidate the release; the listed items are tracked for the
v0.6.x patch release.

- **`bough config validate` reports `unknown field` on the v0.5+
  root sections** (`instinct`, `engines`, `memory_backends`,
  `export`). The validator's first-pass decoder uses
  `internal/config.LegacyConfig` as a v0.3-and-v0.4 field-name
  superset; the v0.5 schema bump did not mirror the new sections
  into that struct, so `validate` rejects a file every other
  subcommand loads cleanly. v0.6.x: extend `LegacyConfig` to mirror
  the v0.5 additions, then keep `migrateLegacy` honest about which
  fields it actually migrates.
- **Layer-confusion clarification in the docs**. v0.6 dogfooding
  uncovered that reviewers (and the maintainers) were conflating
  the 2026 anti-pattern literature on memory CRUD workflows /
  flat-skill libraries with bough's parallel compile target chain.
  `docs/CONCEPTS.md` lands alongside this entry to pin the three
  layers (memory architecture / skill execution orchestration /
  artifact compile chain); `docs/INSTINCTS.md` adds a Related
  projects table that maps each external system to its layer.
- **v0.7 scope reframed to `Bootstrap layer`**. The user-facing
  intent — `claude --worktree X` not only materialises an isolated
  dev environment but also generates the artifacts the next session
  will need — needs a dedicated trigger model above the existing
  CapabilityCompiler. ROADMAP v0.7 entry rewritten accordingly.
- **GoReleaser release pipeline regression**. The v0.6.0 tag's
  first two `release.yml` runs failed because (a) `.goreleaser.
  yaml` carried an unsupported `signs.if` field (removed in
  GoReleaser v2), and (b) the workflow neither installed cosign
  nor granted `id-token: write` for the keyless flow. Fixed in
  e4e1e59 and 6e14237 respectively; the tag was force-moved twice
  to land on the corrected commits before the final
  GoReleaser run succeeded.

## v0.5.1

Drop-in patch on top of v0.5.0 — no schema, plugin contract, or
binary-API changes. Existing plugins and `.bough.yaml` files keep
working unmodified. Three follow-up fixes from the round 3 review
are now live.

### Fixed

- **`instinct.fallback_on_error` is now consumed** (MEDIUM #15). The
  coordinator's `Query` path takes a primary backend error,
  optionally replays the same `QueryReq` against a reference-
  fallback backend, and emits a `query_fallback` audit event. The
  flag was previously schema-only; this wire-up lets a v0.6 mem0
  primary fall back to SQLite without operator intervention.
- **`bough instinct import` / `bough memory import` actually
  restore rows** (MEDIUM #17). The SQLite Import path used to walk
  the YAML / JSONL payload and increment counters without re-
  Storing the rows, so an Import after Forget left the table
  empty. v0.5.1 parses the export shapes into `memapi.Instinct`
  records and routes each through the same Store path the host
  uses for fresh ingest. The CLI also reports
  `imported / upserted / skipped` counts so an operator can
  confirm the round-trip.
- **events.jsonl path must be absolute** (LOW #18).
  `instinct.NewEventWriter` now rejects relative paths up front,
  and `loadInstinctCoordinator` anchors the default
  `.bough/memory/events.jsonl` against the monorepo root that
  `loadConfigAndRoot` resolves. This stops two worktrees (or a CI
  step + a dev shell) from racing on cwd-relative files.

### Notes

- v0.5.0 → v0.5.1 is a drop-in upgrade; no migration steps. The
  MemoryBackend interface is unchanged.
- `internal/instinct.New` gained a fourth parameter (`fallback
  memapi.MemoryBackend`). Callers outside the bough repo (= none
  expected, the package is internal) should pass `nil` for the
  no-op default.

## v0.5.0

The "instinct primitive" release. v0.5 introduces a per-worktree
memory orchestration layer (instinct subsystem) on top of the v0.4
engine plugin model. The subsystem is opt-in — set
`instinct.enabled: true` in `.bough.yaml` to use it. Existing v0.4
monorepos see no behavioural change on upgrade.

**bough is not an agent memory system. bough is a per-worktree
memory orchestration layer.** Memory intelligence is delegated to
external OSS backends (mem0 / Graphiti / Letta, v0.6+); bough
provides the canonical schemas, scope model, safety pipeline
(redaction, poisoning guard, dedupe, decay), and conformance
contract.

### Added

- **Four new plugin contracts**: `plugins/{memory,instinct,
  capability,evaluator}/api/`. v0.5 ships memory (with 7 RPCs:
  Health, Capabilities, Store, Query, Forget, Export, Import)
  and instinct (Mint) as working contracts; capability and
  evaluator are frozen as stubs for v0.6 / v0.7+.
- **Canonical schemas**: `pkg/schema/` declares TraceBundle,
  InstinctCandidate, Instinct, CapabilityArtifact (12 minimal
  fields + Payload json.RawMessage escape hatch), Scope,
  EvidencePolicy, RetrieveBudget.
- **SQLite reference-fallback plugin** (`plugins/memory/sqlite/`):
  modernc.org/sqlite (pure Go, no CGO) + FTS5 + WAL +
  busy_timeout + metadata escape hatch column. Passes the full
  conformance/memory suite (Lifecycle + Bloat + Concurrency).
- **Host coordinator** (`internal/instinct/`): redaction,
  source-aware confidence policy, poisoning guard with hybrid
  mint mode, decay scheduler, scope promote, events.jsonl audit.
- **Stdin ingest** as the PRIMARY observer path:
  `make test 2>&1 | bough instinct ingest --stdin --source test_failure`.
- **Claude `.jsonl` file watch** as opt-in beta with inode-
  rotation + truncate handling (`internal/observer/`).
- **CLI subcommands**: `bough instinct {status, mint, ingest,
  approve, query, forget, promote, export, import}` and
  `bough memory {status, query, forget, export}`. `bough memory
  status` emits a stderr NOTICE when the backend is the SQLite
  reference-fallback so users see the "consider mem0 / graphiti"
  signal every time they probe.
- **Conformance suites**: `conformance/memory/` (Lifecycle,
  Bloat, Concurrency) and `conformance/instinct/` (Lifecycle),
  with in-tree mock plugins and a TestSelf entrypoint.
- **Plugin templates**: `examples/memory-plugin-template/` and
  `examples/memory-plugin-mem0-skeleton/`.
- **Documentation**: `docs/INSTINCTS.md`, `docs/BACKENDS.md`,
  `docs/EXTERNAL_MEMORY_BACKENDS.md`,
  `docs/MEMORY_PLUGIN_AUTHOR_GUIDE.md`,
  `docs/NAMESPACE_MAPPING.md`, `docs/SECURITY.md`,
  `docs/ROADMAP.md`.

### Removed (breaking for v0.3 plugin binaries)

- **`internal/pluginhost`** drops the v0.3 DBProvider fallback
  handshake. v0.3.x plugin binaries no longer spawn under a v0.5
  host. Users running an old plugin binary must rebuild against
  `plugins/engine/api/` from v0.4.0 or later. The legacy
  `pickDatabaseNames` helper and `legacyEngineAdapter` are also
  removed.

### Changed

- `.bough.yaml` schema gains `instinct:`, `memory_backends:`, and
  `export:` sections. All are opt-in (empty/absent → subsystem
  disabled). `schema_version` stays at 2.
- GoReleaser now produces 6 binaries: the existing host + four
  engine plugins, plus the new `bough-plugin-memory-sqlite`.
- CI matrix splits engine-conformance and memory-conformance into
  separate jobs so the SQLite plugin's WAL / concurrency tests do
  not contend with the engine plugin's docker pre-pull.

## v0.4.1

Docs / user-visible-string cleanup follow-up to v0.4.0. No
behaviour change. Most strings that still read like v0.3 were
updated; v0.3 references in CHANGELOG history, MIGRATION docs,
fallback impl, and the legacy migrateLegacy() test fixture stay
intentional.

### Changed (docs / strings only)

- **cobra help text** (`bough --help`, `bough config --help`,
  `bough plugins --help`, `bough backfill --help`, `bough list --help`):
  now points at `.bough.yaml` / `.bough-ports.json` / `~/.bough/backups/`
  with a one-clause "v0.3 ... accepted on fallback" note. `bough plugins`
  reads "List engine plugins discoverable on PATH" (was "List DB
  plugins").
- **Rendered `.env.local` footer** is now `# Do not commit. Manage via
  '.bough.yaml' at the monorepo root.` (every freshly-created worktree
  picks up the new wording on the next `bough create`).
- **`backend.Detect` error message** now points at `engines[].backend`
  in `.bough.yaml` instead of `databases[].backend` in
  `.worktree-isolation.yaml`.
- **Doc comments** in `internal/backend/`, `internal/envwriter/`,
  `internal/gitwt/` updated to v0.4 canonical names.
- **`tests/integration/e2e_mysql_test.go`** fixture bumped to v0.4
  canonical (`schema_version: 2` / `engines:` / `port_ranges:` /
  `initial_resources:` / `role: engine-provider` / `.bough.yaml` /
  `.bough-ports.json` / registry key `mysql.main`). The v0.3 →
  v0.4 migrateLegacy() path stays covered by the existing
  `config_test.go` unit tests.

### Docs

- **README.md** Quick start uses `.bough.yaml` + schema_version 2 +
  `engines:` + `port_ranges:` + `initial_resources:`. Status section
  reflects v0.4.0 reality. Prerequisites now spells out the auto-detect
  order (nix → docker, deliberate v0.1.x compat preference) and the
  table puts docker first (= typical install).
- **`docs/PLUGIN_AUTHOR_GUIDE.md`** gains a Multi-port engines section
  (rabbitmq AMQP+Management, kafka broker+controller, NATS
  client+monitor+cluster) with the rabbitmq author's view of
  `PortRangeDefault` / `Up` / `EnvVars` / `Config.MainPortRole`.
- **`docs/MIGRATION-v0.3-to-v0.4.md`** past-tenses "v0.4.0 will keep
  working" → "keeps working" now that v0.4.0 has shipped.
- **`examples/plugin-template/`** README + conformance_test.go gain a
  Multi-port section pointing at the PLUGIN_AUTHOR_GUIDE walkthrough
  and a `MainPortRole` TODO marker.
- **`plugins/db/api/CONTRACT.md`** deleted — superseded by
  `plugins/engine/api/CONTRACT.md`. The legacy Go fallback files
  stay for v0.3.x plugin binary compat.

### Refactor (developer-only)

- **`internal/smoketool/`** extracts the shared Up → ReadyCheck → Down →
  Cleanup lifecycle so the four `cmd/_smoke-docker-<kind>/` binaries
  shrink to ~15-line `main()` calls that only spell out their plugin
  and per-engine tunables.
- **`conformance/lifecycle.go::runLifecycle`** 172 → 50 lines via per-
  phase helpers (`runUpPhase` / `runReadyCheckPhase` / `runEnvVarsPhase`
  / `runDownPhase` / `runOneIteration` / `assertCleanup`).
- **`internal/cli/create.go::runCreate`** 211 → 70 lines via
  `allocateAllPorts` / `startEngines` / `materializeRepositories` /
  `renderEnvLocals` / `runPostCreateHooks` / `detectBackendIfNeeded`.
  The awkward `interface{ Write([]byte) (int, error) }` parameter type
  is replaced with `io.Writer`.

### CI

- `.github/workflows/ci.yml` conformance matrix points at
  `./plugins/engine/<plugin>/...` (was missed when `plugins/db/` was
  renamed to `plugins/engine/` in v0.4.0; the previous post-v0.4.0
  matrix runs against ad-hoc PRs would not have caught this).

## v0.4.0

bough generalises from "DB plugin orchestrator" to "engine plugin
orchestrator". Middleware (rabbitmq, kafka, nats, minio, …) can now be
written as plugins on the same lifecycle as the existing DB plugins.
The breaking changes are intentionally collected into one release so
plugin authors pay the cost once. The v0.4.x line keeps fallbacks for
every renamed surface so existing deployments do not have to migrate
in lockstep — they will be removed in v0.5.0. See
[`docs/MIGRATION-v0.3-to-v0.4.md`](./docs/MIGRATION-v0.3-to-v0.4.md)
for the full diff.

### Changed (breaking, with fallback)

- **`DBProvider` → `EngineProvider`.** The gRPC contract is renamed,
  `UpReq.Port int` becomes `UpReq.Ports []PortSpec`, `InitialDatabases
  []string` becomes `InitialResources []ResourceSpec`,
  `PortRangeDefault` returns `map[string]PortRange` (one entry per
  role). Single-port plugins keep the v0.3 shape via `Role: "main"`
  (or empty, treated identically). See
  [`plugins/engine/api/CONTRACT.md`](./plugins/engine/api/CONTRACT.md).
- **`plugins/db/` → `plugins/engine/`.** The four bundled plugins
  (mysql / postgres / redis / elasticsearch) move to the new path.
  External plugins need to update their Go module import from
  `github.com/ikeikeikeike/bough/plugins/db/api` to
  `github.com/ikeikeikeike/bough/plugins/engine/api`.
- **YAML schema_version 2.** `databases:` → `engines:`,
  `port_range: [a, b]` → `port_ranges: { main: [a, b] }`,
  `initial_databases: ["foo"]` → `initial_resources: [{ type:
  database, name: foo }]`. The host loader still accepts the v0.3
  field names with a deprecation warning and converts them in memory.
- **File / dir / handshake renames.**
  `.worktree-isolation.yaml` → `.bough.yaml`,
  `.worktree-ports.json` → `.bough-ports.json`,
  `~/.claude/backups/` → `~/.bough/backups/`,
  gRPC handshake magic cookie `BOUGH_DB_PLUGIN` → `BOUGH_ENGINE_PLUGIN`.
  Every old surface is read/honoured during the v0.4.x line; the host
  attempts the new handshake first and falls back to the v0.3 one so
  v0.3.x plugin binaries still spawn under a v0.4.x host.
- **Repository role rename.** `role: db-provider` →
  `role: engine-provider` (the YAML accepts both during v0.4.x).
- **Registry composite key.** Engine entries are now stored as
  `<kind>.<role>` (e.g. `mysql.main`); legacy keys without a dot are
  upgraded on load so existing `.worktree-ports.json` files keep
  their port allocations.

### Added

- **Multi-port engines.** Plugins declare one role per listen point
  from `PortRangeDefault`; the host allocates a deterministic port
  per role; `EnvVars` emits `BOUGH_<ENGINE>_HOST` (shared) plus
  `BOUGH_<ENGINE>_<ROLE>_PORT` / `_URL` per role. The conformance
  suite exercises the full multi-port lifecycle end-to-end against
  the in-tree mock plugin (`TestRun_MockPlugin_MultiPort_GreenPath`).
- **`conformance.Config.MainPortRole`** (default `"main"`). Targets
  the fault tests at a single role on multi-port plugins; the
  lifecycle still iterates over every declared role.
- **`AssertReachable` longest-prefix host lookup.** A `*_<ROLE>_PORT`
  key now pairs back to the nearest ancestor `*_HOST` instead of
  requiring a per-role `*_HOST` duplicate.
- **Shim helpers** `api.PickMainPort([]PortSpec)` and
  `api.PickFirstResourceName([]ResourceSpec, type)` in
  `plugins/engine/api/shims.go` keep single-port engine internals
  signature-compatible with the v0.3.x docker/nix code.
- **`docs/MIGRATION-v0.3-to-v0.4.md`** — side-by-side YAML +
  plugin-author checklist + fallback table + v0.5.0 removal timeline.
- **`docs/PLUGIN_AUTHOR_GUIDE.md` multi-port section** — rabbitmq
  author's view of `PortRangeDefault` / `Up` / `EnvVars` /
  `MainPortRole`.
- **`examples/plugin-template`** — Multi-port section in README,
  `MainPortRole` TODO marker in the conformance template, canonical
  paths throughout.

### Notes for plugin maintainers

Existing v0.3.x plugin binaries still spawn under v0.4.x via the
fallback handshake. To target v0.4 natively:

1. Update the import path
   (`plugins/db/api` → `plugins/engine/api`).
2. Switch the lifecycle signatures (`req *UpReq` taking `Ports
   []PortSpec`; `ReadyCheck(ctx, ports []int, ...)`; `Cleanup(ctx,
   datadir string, ports []int)`; `PortRangeDefault` returning
   `map[string]PortRange`).
3. Rebuild and re-run the conformance suite against bough/conformance
   v0.4.0.

The v0.5.0 release removes the v0.3 fallbacks — plugins that have
not been rebuilt by then will stop loading.

## v0.3.0

### Added

- **Plugin conformance suite** (`bough/conformance`) — plugin authors verify
  their go-plugin server against the bough contract with one test function:

  ```go
  //go:build conformance
  func TestMyPluginConformance(t *testing.T) {
      conformance.Run(t, conformance.Config{
          PluginBinary: os.Getenv("BOUGH_CONFORMANCE_PLUGIN_BIN"),
          Image:        "myengine:1.0",
      })
  }
  ```

  The suite spawns the binary under hashicorp/go-plugin (the same path
  bough's host uses in production), drives `PortRangeDefault → Up →
  ReadyCheck → EnvVars → Down → Cleanup` with idempotency, asserts the
  v0.2.5 (shell metachar) and v0.2.6 (container bridge-IP advertise)
  invariants mechanically, and runs three fault injections (port
  conflict, datadir permission, image pull failure).

- **CI conformance matrix** (`.github/workflows/ci.yml`) — every PR runs
  the suite on `ubuntu-24.04` + `ubuntu-24.04-arm` × `mysql` /
  `postgres` / `redis` / `elasticsearch`, eight cells in parallel.

- **`Makefile`** gains `conformance-local PLUGIN=<kind>` and
  `conformance-all` targets so plugin authors can verify against
  Docker Desktop / OrbStack / Colima on macOS.

- **Plugin author template** (`examples/plugin-template/`) — copy this
  directory and fill in four TODO markers to start a new plugin.

- **Plugin contract documentation** (`plugins/db/api/CONTRACT.md`) and
  **author guide** (`docs/PLUGIN_AUTHOR_GUIDE.md`).

### Fixed

- The bough host's `internal/pluginhost` exposes `DiscoverFromBinary` so
  the conformance suite can pin an exact binary path instead of
  relying on PATH lookup. Existing `Discover(kind)` wraps it.

### Plugin author notes

`conformance.Config.AllowShellMetachars=true` is the opt-out for plugins
whose URL/DSN values legitimately contain `(`, `&`, `?` (the go-sql-
driver mysql DSN format). `Skip{PortConflict,DatadirPermission,
ImagePullFailure}` are the per-fault opt-outs for backends that cannot
simulate the corresponding error path.

The four bough-internal plugins all set `SkipDatadirPermission=true`
because they only bind-mount `Datadir`; the engine inside the
container writes there, so a host-side chmod 0o000 crashes the engine
after `Up` has already returned. The downstream symptom is covered by
`AssertReachable` + `NativeProbe`.

### Follow-ups (not in v0.3.0)

- `bough conformance` CLI wrapper around `testing.MainStart` — plugin
  authors get the same coverage via `go test -tags=conformance` today.
- Plugin-side `Cleanup` chown helper so `os.RemoveAll` succeeds on
  Linux runners even after a container wrote files as a non-host uid.
  The conformance suite currently works around this with its own
  `docker run --rm alpine chown` fallback (see `conformance/datadir.go`).
- Conformance suite for the nix-services-flake backend; v0.3.0 forces
  `extras["backend"]="docker"` so the docker side is what CI verifies.

## v0.2.6

- **fix(plugins/db/elasticsearch)**: advertise host-reachable publish
  address — sniffing clients (olivere/elastic et al.) used to dial the
  container's bridge IP and crash boot.

## v0.2.5

- **fix(create)**: inject `.env.local` `KEY=VALUE` pairs directly into
  `post_create` child env. The previous `source .env.local` shelled out
  to bash, which aborted on the first `(` in any value (mysql DSN) and
  silently emptied every later `${VAR}`.

## v0.2.4

- **feat(gitwt,cli)**: fetch origin and branch off `origin/<base>` on
  `bough create` so a stale local develop does not get inherited.

## v0.2.0

- Docker backend implementation + hybrid backend selector
  (`auto-detect` on `nix` first, then `docker`).

## v0.1.1

- Bundled `flake.lock` per plugin (cold start 5-10 min → 30-60 s);
  `packages.default` for `nix run` / `nix profile install`; per-engine
  `ready_timeout_sec` config.

## v0.1.0

- First public release. Nix `services-flake` backend; 4 DB plugins
  (mysql / postgres / redis / elasticsearch); cobra CLI;
  `.worktree-isolation.yaml`-driven host.
