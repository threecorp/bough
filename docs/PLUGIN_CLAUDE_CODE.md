# bough as a Claude Code plugin

This repo doubles as a [Claude Code plugin](https://code.claude.com/docs/en/plugins.md)
so bough can be driven from inside a session (`/bough:*` slash commands) instead
of only from a shell. This is separate from bough's own gRPC **engine** plugins
(`bough-plugin-<kind>`, see [`PLUGIN_AUTHOR_GUIDE.md`](./PLUGIN_AUTHOR_GUIDE.md));
"plugin" here means the Claude Code kind.

## Layout

```text
.claude-plugin/
  marketplace.json   # the marketplace catalog (one plugin: bough, source "./")
  plugin.json        # the plugin manifest (name: bough)
commands/            # slash commands — one .md per /bough:<name>
  create.md remove.md list.md status.md verify.md doctor.md
  instinct-status.md instinct-list.md instinct-promote.md evolve.md config-validate.md
skills/
  using-bough/SKILL.md   # model-invoked orchestration + PATH preflight
hooks/
  hooks.json         # wires all 8 events to `bough hook handle --event <E>`
```

`plugin.json` omits the `commands` / `skills` / `hooks` fields on purpose: Claude
Code auto-discovers the default `commands/`, `skills/`, and `hooks/hooks.json`
directories at the plugin root. `version` is omitted too, so the plugin tracks
the git commit SHA (every push is a new version) rather than needing a manual
bump per command edit.

## Command / skill / hook reference

### Slash commands (`/bough:<name>`) — you type these

| Command | What it does | Args |
|---|---|---|
| `/bough:create` | Create a per-worktree isolated env (its own MySQL/Redis/ES + a rendered `.env.local` per sub-repo). | `<worktree-name>` |
| `/bough:remove` | Tear one down (stops engines, drops datadirs, removes the worktree; keeps the branch). | `<name-or-path>` |
| `/bough:list` | List the worktrees registered in `.bough-ports.json`. | — |
| `/bough:status` | Registry + live `lsof` listen state per port (spot stopped engines / collisions). | — |
| `/bough:verify` | Report drift between a worktree's registry, its `.env.local`, and the declared ranges. | `<name>` |
| `/bough:config-validate` | Validate a `.bough.yaml` against the schema. | `[path]` |
| `/bough:instinct-status` | Per-project instinct totals + confidence histogram. | — |
| `/bough:instinct-list` | List instincts (id, trigger, confidence, domain). | — |
| `/bough:instinct-promote` | Promote cross-project instincts (≥2 projects, avg confidence ≥0.8) into the global corpus. Previews with `--dry-run`. | `[--dry-run]` |
| `/bough:evolve` | Cluster instincts into skills/agents/commands via the 5-gate pipeline (`--generate` to emit; GATE 5 runs an LLM judge on your Claude subscription). | `[--generate]` |
| `/bough:doctor` | Report hook wiring, observer state, and cost posture; warns on double-wiring. | — |

### Skill — Claude invokes this on its own

| Skill | What it does |
|---|---|
| `using-bough` | Model-invoked guidance on which `/bough:*` fits the user's intent, plus a `command -v bough` PATH preflight that stops with install guidance when the binary is missing. |

### Hooks — fire automatically (no command)

Installing the plugin wires all eight events to `bough hook handle --event <E>`:

| Event | Effect |
|---|---|
| PreToolUse / PostToolUse | record the tool call into the homunculus `observations.jsonl` |
| UserPromptSubmit | inject the confidence-ranked instinct block into the next turn |
| SessionEnd | summarise the session + re-score instinct confidence (±) |
| PreCompact | snapshot the top-N instincts before compaction |
| Stop | record an observation |
| WorktreeCreate / WorktreeRemove | run `bough create` / `bough remove` for `claude --worktree` |

LLM instinct **minting** (`bough observer start`) stays opt-in — none of the hooks
above spawn it.

## The binary is a prerequisite, not bundled

The plugin ships markdown + JSON only. Every command shells out to `bough` on
`PATH`; the binary comes from a GitHub release / `nix` / `go install` (see the
README **Install** section). The `using-bough` skill runs a `command -v bough`
preflight and stops with install guidance if it is missing.

## The hook manifest is kept honest

`hooks/hooks.json` mirrors, verbatim, the command `bough hook install` writes
into `settings.json`: every event in `hooks.AllEvents()` mapped to
`hooks.CanonicalCommand(event)` (`bough hook handle --event <E>`). Because it is
a hand-authored static file, it could silently drift when the event set changes.

`internal/hooks/plugin_sync_test.go` is the guard: it parses the committed
`hooks/hooks.json` and fails CI if any event is missing, wires a non-canonical
command, or declares an event absent from `AllEvents()`. Update the manifest and
the canonical wiring in lockstep, or the test goes red.

## Don't double-wire

Installing the plugin wires the hooks; running `bough hook install` also wires
them into `settings.json`. Both present means every event fires twice. Use one:
for the plugin, run `bough hook uninstall` to drop the `settings.json` copy.
`bough doctor` prints a heads-up when it detects bough hooks in `settings.json`.
LLM instinct minting stays opt-in either way (`bough observer start`).
