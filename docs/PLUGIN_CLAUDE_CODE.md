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
```

The plugin ships **commands + skill only — no `hooks/`**. Hooks fire on every
event and belong to the specific project you want observed, so they are wired by
the CLI (`bough hook install`), not forced on by installing the plugin (see
[Hooks are not in the plugin](#hooks-are-not-in-the-plugin-on-purpose) below).

`plugin.json` omits the `commands` / `skills` fields on purpose: Claude Code
auto-discovers the default `commands/` and `skills/` directories at the plugin
root. `version` is omitted too, so the plugin tracks the git commit SHA (every
push is a new version) rather than needing a manual bump per command edit.

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
| `/bough:doctor` | Report hook wiring, observer state, and cost posture. | — |

### Skill — Claude invokes this on its own

| Skill | What it does |
|---|---|
| `using-bough` | Model-invoked guidance on which `/bough:*` fits the user's intent, plus a `command -v bough` PATH preflight that stops with install guidance when the binary is missing. |

### Hooks — wired separately (NOT by the plugin)

The plugin does **not** wire hooks. Observation is opt-in per project via the
CLI:

```text
bough hook install --scope project      # this repo's .claude/settings.json  (recommended)
bough hook install --scope user         # ~/.claude/settings.json (observe every repo)
```

That wires all eight events to `bough hook handle --event <E>`:

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

## Hooks are not in the plugin (on purpose)

Earlier versions shipped a `hooks/hooks.json` so `/plugin install` also wired the
observe/inject loop. That was dropped: a plugin installs at **user scope** by
default, so its hooks would fire in *every* repo — including a repo that already
runs its own learning loop, where a second observer double-records and injects an
unwanted instinct block. Commands and the skill are inert until invoked, so they
are safe user-scoped; hooks are not.

So observation is wired by the CLI, scoped to the project you choose:

```text
bough hook install --scope project      # this repo only  (recommended)
bough hook install --scope user         # every repo (if that is genuinely what you want)
bough hook uninstall
```

`bough hook install` writes every event in `hooks.AllEvents()` mapped to
`hooks.CanonicalCommand(event)` (`bough hook handle --event <E>`) into the chosen
`settings.json`. `bough doctor` shows exactly what is wired there. This keeps the
"install the plugin everywhere / observe only where you asked" split clean, with
no double-firing to reconcile.
