# bough as a Claude Code plugin

This repo doubles as a [Claude Code plugin](https://code.claude.com/docs/en/plugins.md)
so bough can be driven from inside a session (`/bough:*` slash commands) instead
of only from a shell. This is separate from bough's own gRPC **engine** plugins
(`bough-plugin-<kind>`, see [`PLUGIN_AUTHOR_GUIDE.md`](./PLUGIN_AUTHOR_GUIDE.md));
"plugin" here means the Claude Code kind.

## Pick a variant

The marketplace publishes three, because the artifacts differ in *when they act*
and that is the only thing worth choosing between:

| Plugin | Ships | Install when |
|---|---|---|
| `bough` | commands + skill | You want `/bough:*` on hand. Inert until invoked, so it is safe at any scope. |
| `bough-hooks` | hooks | You drive bough from the shell and only want the observe/inject loop. |
| `bough-all` | commands + skill + hooks | You want the lot in one line. |

```text
/plugin marketplace add threecorp/bough
/plugin install bough-all@bough          # or bough@bough / bough-hooks@bough
```

A hook fires on **every** session event in whatever scope it is installed at, so
install any variant that carries hooks at **project scope** unless observing
every repo on the machine is genuinely what you want:

```text
claude plugin install bough-all@bough --scope project   # this repo only  (recommended)
claude plugin install bough-all@bough --scope user      # every repo
```

Project scope writes `enabledPlugins` into that repo's own
`.claude/settings.json`, and the hooks fire only in sessions started there. (The
`/plugin install` TUI offers the same choice; the `claude plugin install` CLI
defaults to user scope when `--scope` is omitted, so pass it.)

## Layout

```text
.claude-plugin/
  marketplace.json   # the catalog: bough / bough-hooks / bough-all
  plugin.json        # manifest for the root `bough` plugin
commands/            # slash commands — one .md per /bough:<name>
  create.md remove.md list.md status.md verify.md doctor.md
  instinct-status.md instinct-list.md instinct-promote.md evolve.md config-validate.md
skills/
  using-bough/SKILL.md   # model-invoked orchestration + PATH preflight
claude-plugins/
  bough-hooks/
    .claude-plugin/plugin.json
    hooks/hooks.json     # the ONE copy of the hook manifest
  bough-all/
    .claude-plugin/plugin.json
    commands -> ../../commands     # symlinks: one copy of every artifact
    skills   -> ../../skills
    hooks    -> ../bough-hooks/hooks
```

`bough-all` symlinks rather than copies so the three variants cannot ship
different content under one version. `marketplace_test.go` and
`internal/hooks/plugin_sync_test.go` assert that: a copy in place of a symlink
fails the build.

Each `plugin.json` omits the `commands` / `skills` / `hooks` fields on purpose —
Claude Code auto-discovers those directories at the plugin root. `version` is
omitted too, so a plugin tracks the git commit SHA (every push is a new version)
rather than needing a manual bump per command edit.

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

### Hooks (`bough-hooks` / `bough-all`)

All eight events map to `bough hook handle --event <E>`:

| Event | Effect |
|---|---|
| PreToolUse / PostToolUse | record the tool call into the homunculus `observations.jsonl` |
| UserPromptSubmit | inject the confidence-ranked instinct block into the next turn |
| SessionEnd | summarise the session + re-score instinct confidence (±) |
| PreCompact | snapshot the top-N instincts before compaction |
| Stop | record an observation |
| WorktreeCreate / WorktreeRemove | run `bough create` / `bough remove` for `claude --worktree` |

LLM instinct **minting** (`bough instinct observer start`) stays opt-in — none of the
hooks above spawn it.

## The CLI installs the same artifacts

Everything the plugins ship is embedded in the binary, so you can install any
kind without a marketplace round-trip — useful when you want an artifact at a
scope the plugin flow does not offer, or want no plugin at all:

```text
bough claude hook install --scope project      # .claude/settings.json
bough claude skill install --scope project     # .claude/skills/
bough claude command install --scope project   # .claude/commands/
bough claude <kind> list | uninstall           # same verbs for all three
```

`uninstall` removes only the entries bough ships; anything you authored in the
same place is left alone.

Two notes on the CLI path:

- Commands installed this way are **flat** (`/create`, not `/bough:create`) —
  filesystem commands are not namespaced, only plugin ones are.
- Project scope lands where `bough create` points each worktree's
  `.claude/<kind>` symlink, so installing once at the monorepo root reaches the
  worktree sessions too.

## Pick one wiring for hooks, not both

`bough claude hook install` and the `bough-hooks` / `bough-all` plugins wire the
**same dispatcher** by two different routes. Both at once fires every event
twice: doubled observations, a doubled instinct block. bough cannot read the
Claude Code plugin registry, so it cannot detect the conflict for you —
`bough claude doctor` prints a note whenever settings.json carries bough hooks,
and `claude plugin list` shows the other half.

Keep whichever you prefer and drop the other:

```text
bough claude hook uninstall     # keep the plugin's wiring
# ...or remove the plugin and keep settings.json
```

## The binary is a prerequisite, not bundled

The plugins ship markdown + JSON only. Every command shells out to `bough` on
`PATH`; the binary comes from a GitHub release / `nix` / `go install` (see the
README **Install** section). The `using-bough` skill runs a `command -v bough`
preflight and stops with install guidance if it is missing.
