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
| `bough-hooks` | hooks | You drive bough from the shell and only want `claude --worktree` wired. |
| `bough-all` | commands + skill + hooks | You want the lot in one line. |

```text
/plugin marketplace add threecorp/bough
/plugin install bough-all@bough          # or bough@bough / bough-hooks@bough
```

The hooks fire in whatever scope they are installed at, and they act on the
monorepo they find themselves in, so install any variant that carries hooks at
**project scope** unless you want `claude --worktree` wired in every repo on the
machine:

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
  create.md remove.md list.md status.md verify.md doctor.md config-validate.md
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
| `/bough:doctor` | Report hook wiring, engine-plugin posture, and any wiring left over from a retired feature. | — |

### Skill — Claude invokes this on its own

| Skill | What it does |
|---|---|
| `using-bough` | Model-invoked guidance on which `/bough:*` fits the user's intent, plus a `command -v bough` PATH preflight that stops with install guidance when the binary is missing. |

### Hooks (`bough-hooks` / `bough-all`)

Both events map to `bough hook handle --event <E>`:

| Event | Effect |
|---|---|
| WorktreeCreate | run `bough create` and print the worktree root for `claude --worktree` to `cd` into |
| WorktreeRemove | run `bough remove` — stop the engines, drop the datadirs, remove the worktree |

Nothing else is wired, so a session that never uses `claude --worktree` never
runs bough. Versions up to v0.26.0 also wired `PreToolUse`, `PostToolUse`,
`UserPromptSubmit`, `Stop`, `SessionEnd` and `PreCompact` for a continuous-learning
loop that no longer exists; they now do nothing, and one
`bough claude hook install` prunes them out of `settings.json`.

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
- Project scope lands at the monorepo root's own `.claude/<kind>`. As of
  v0.27.0 `bough create` symlinks only `CLAUDE.md` into a worktree, so a
  worktree session does NOT pick these up — install them user-scoped, or wire
  your own symlink, if you want them there.

## Pick one wiring for hooks, not both

`bough claude hook install` and the `bough-hooks` / `bough-all` plugins wire the
**same dispatcher** by two different routes. Both at once fires every event
twice, so one `claude --worktree` runs `bough create` twice.

`bough claude doctor` detects it. Claude Code records an enabled plugin as
`enabledPlugins` in the same `settings.json` bough already manages, so when both
halves are in that file the doctor says so outright:

```text
  WARNING: bough's hooks are wired twice — here, and by bough-all@bough.
    Both fire: one `claude --worktree` runs `bough create` twice, and
    the second run trips over the worktree the first one made. Keep one —
      bough claude hook uninstall     (keep the plugin's wiring)
      ...or drop the plugin side, which means ALL of these — uninstalling
      one of two leaves the other still firing:
        claude plugin uninstall bough-all@bough
```

One limit worth knowing: the doctor reads the `settings.json` for the scope it
was asked about. A plugin enabled at the *other* scope (user vs project) is
outside what it can see, so it says that rather than implying all-clear —
`claude plugin list` shows the rest.

## The binary is a prerequisite, not bundled

The plugins ship markdown + JSON only. Every command shells out to `bough` on
`PATH`; the binary comes from a GitHub release / `nix` / `go install` (see the
README **Install** section). The `using-bough` skill runs a `command -v bough`
preflight and stops with install guidance if it is missing.
