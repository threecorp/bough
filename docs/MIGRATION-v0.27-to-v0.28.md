# bough v0.27.0 → v0.28.0 migration guide

v0.28.0 removes the continuous-learning loop. bough is a per-worktree
isolation tool again: git worktrees, deterministic ports, per-worktree
engines, a rendered `.env.local`, and the two `claude --worktree`
hooks. Learning belongs to a Claude Code plugin, and bough has no
business shipping a second implementation of one.

Nothing about the isolation core changed. If you never ran
`bough instinct` or `bough evolve`, the upgrade is one command:

```bash
bough claude hook install     # prunes the six retired hook events
```

## TL;DR

| You have | What happens on v0.28.0 | What to do |
|---|---|---|
| Six retired events in `settings.json` | Each fires, prints one stderr line, exits 0 | `bough claude hook install` |
| `instinct:` / `quality_gates:` / `memory_backends:` / `export:` in `.bough.yaml` | Read, warned about once per load, otherwise ignored | Delete the section before v0.29.0 |
| `~/.local/share/bough-homunculus/` | Never read, never written | Yours to keep or delete |
| Scripts calling `bough instinct …` / `bough evolve` / `bough ops` | `unknown command` | Pin v0.27.0, or drop the call |
| The `bough-hooks` / `bough-all` Claude Code plugin | Updates to two events when you update the plugin | `/plugin update`, or nothing |

The compatibility shims above last one minor series. **v0.29.0 makes a
retired `.bough.yaml` key a hard validation error and drops the hook
shim**, so a retired event would then exit non-zero.

## Hook wiring

v0.27.0 wired eight Claude Code events. v0.28.0 wires two:
`WorktreeCreate` and `WorktreeRemove`.

`PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `Stop`, `SessionEnd`
and `PreCompact` are **retired**. Wiring left behind in `settings.json`
still runs — the host fires it on every tool call — and each run exits
0 with one line on stderr:

```text
[bough] hook event PreToolUse is retired since v0.28.0 and does nothing; run `bough claude hook install` to prune the stale wiring
```

Claude Code does not show a successful hook's stderr, so this is
invisible in a session and costs one short process per event. Prune it:

```bash
bough claude hook install --scope project   # or --scope user
```

`install` writes the two live events and deletes bough's own entries
for the six retired ones, including the now-empty event keys. **A hand
written entry on a retired event is left alone**, and so is a group
that mixes yours with bough's — the same rule `uninstall` has always
followed. `bough claude doctor` names whatever is left.

An **unknown** event is now an error rather than a silent success:

```text
unknown hook event "PreToolUsee" (wired: WorktreeCreate, WorktreeRemove)
```

In v0.27.0 a typo exited 0 with empty stdout, which the host reports as
"hook succeeded but returned no worktree path" — a failure two steps
away from its cause.

## `.bough.yaml`

Four top-level sections are retired: `instinct:`, `quality_gates:`,
`memory_backends:` and `export:`. They are read and discarded, and each
one present prints one line per load:

```text
bough: WARNING YAML section 'instinct:' is retired and does nothing: the continuous-learning loop it configured was removed in v0.28.0; delete the section (the key stops parsing in v0.29.0)
```

Delete the sections. Everything else in the file is unchanged, and an
unknown key is still a hard error — tolerating these four did not
loosen the schema:

```text
parse .bough.yaml: yaml: unmarshal errors:
  line 3: field repositries not found in type config.LegacyConfig
```

`instinct.plugin_security` went with the rest. It configured engine
plugin signature verification, but nothing has ever read it (see
[SIGNING.md](./SIGNING.md)); when signature enforcement is wired it
gets a top-level key of its own.

## Commands that are gone

| v0.27.0 | v0.28.0 |
|---|---|
| `bough instinct status` / `list` / `show` / `promote` | gone |
| `bough instinct observer run-once` / `start` / `stop` / `status` | gone |
| `bough instinct evolve [--generate]` | gone |
| `bough instinct import` | gone |
| `bough instinct verdict keep` / `retire` / `done` | gone |
| `bough ops` | gone |
| `bough observer` / `bough evolve` / `bough ecc` (deprecated aliases) | gone |
| `bough inject-context` / `session-end` / `preserve-instincts` / `session-evolve-claudemd` (hidden) | gone |
| `/bough:instinct-status` / `instinct-list` / `instinct-promote` / `evolve` | gone |

Everything else keeps its name and its flags: `create`, `remove`,
`verify`, `list`, `status`, `backfill`, `repair`, `config validate`,
`plugins list`, `claude hook|skill|command install|uninstall|list`,
`claude doctor`, and `hook handle`.

`bough claude doctor` lost its continuous-learning block and gained a
**Retired state** section that names leftover wiring, leftover
`.bough.yaml` sections, and the corpus directory if it is still there.

## On-disk state

`~/.local/share/bough-homunculus/` (or wherever
`BOUGH_HOMUNCULUS_DIR` pointed) is left exactly as it is. v0.28.0 never
reads or writes it. `bough claude doctor` mentions it once so it does
not sit there unexplained; deleting it is your call, and nothing in
bough will do it for you.

Evolved artifacts that v0.27.0 wrote into `.claude/skills`,
`.claude/agents` and `.claude/commands` are ordinary files that Claude
Code still loads. bough no longer writes or links them, and `bough
create` no longer symlinks a worktree's `.claude/{skills,agents,commands}`
at the monorepo copy — only `CLAUDE.md`, which was never part of the
loop.

## Staying on the loop

Pin **v0.27.0**. It is the last release that carries it:

```bash
go install github.com/ikeikeikeike/bough/cmd/bough@v0.27.0
# or: nix profile install github:threecorp/bough/v0.27.0
```

The design notes live in [attic/](./attic/) — [EVOLVE.md](./attic/EVOLVE.md)
for the five-gate pipeline, [QUARANTINE-REVIEW.md](./attic/QUARANTINE-REVIEW.md)
for the denylist review flow.
