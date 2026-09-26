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

If the `bough-hooks` or `bough-all` plugin is enabled at any scope, run
`bough claude hook uninstall` instead: the plugin already wires the two
live events, and `install` would wire them a second time.

## TL;DR

| You have | What happens on v0.28.0 | What to do |
|---|---|---|
| Six retired events in `settings.json` | Each fires, prints one stderr line, exits 0 | `bough claude hook install` |
| `instinct:` / `quality_gates:` / `memory_backends:` / `export:` in `.bough.yaml` | Read, warned about once per load, otherwise ignored | Delete the section before v0.29.0 |
| `~/.local/share/bough-homunculus/` | Never read, never written | Yours to keep or delete |
| A running observer daemon (`bough instinct observer start`, or `observer.autostart`) | Keeps running on the old binary image; v0.28.0 cannot stop it | Stop it first — see [On-disk state](#on-disk-state) |
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
[bough] hook event PreToolUse is retired since v0.28.0 and does nothing; run `bough claude hook install` to prune it from settings.json, or `claude plugin update bough-hooks` (or bough-all) if the wiring comes from the plugin
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

If you copied the slash commands in with `bough claude command install`,
v0.28.0's `uninstall` no longer knows these four names. Delete them by hand
at each scope you installed to (`.claude/commands/` in the project,
`~/.claude/commands/` for user scope):

```bash
rm -f .claude/commands/{evolve,instinct-list,instinct-promote,instinct-status}.md
```

Everything else keeps its name and its flags: `create`, `remove`,
`verify`, `list`, `status`, `backfill`, `repair`, `config validate`,
`plugins list`, `claude hook|skill|command install|uninstall|list`,
`claude doctor`, and `hook handle` (which lost only its `--out` flag).

`bough claude doctor` lost its continuous-learning block and gained a
**Retired state** section that names leftover wiring, leftover
`.bough.yaml` sections, and the corpus directory if it is still there.

## On-disk state

**Stop the observer daemon before you upgrade.** It is a detached process
that v0.28.0 has no command to stop, and it carries on from the old binary
already in memory. On v0.27.0 or earlier, in each monorepo that started one:

```bash
bough instinct observer stop
```

If you have already upgraded, find it by its command line and stop it:

```bash
pgrep -fl 'observer _run-daemon'      # one line per monorepo it was started for
pkill -f 'observer _run-daemon'
```

Its PID is also in `~/.local/share/bough-homunculus/projects/<project-id>/observer.pid`.
`bough claude doctor` names any PID from those files that is still alive.

`~/.local/share/bough-homunculus/` (or wherever
`BOUGH_HOMUNCULUS_DIR` pointed) is left exactly as it is. v0.28.0 never
writes or deletes anything there; the only read is `bough claude doctor`
checking its `observer.pid` files. `bough claude doctor` mentions it once so it does
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
