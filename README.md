# bough

> Per-worktree isolation orchestrator for monorepos.

`bough` brings up an isolated dev environment per git worktree (per
feature branch): a deterministically-allocated port set, a rendered
`.env.local` in every sub-repo that declares `env_local`, and a
worktree-local instance of every declared engine — all driven by one
`.bough.yaml` at the monorepo root.

bough itself is a small Go CLI plus five engine plugins
(`bough-plugin-{mysql,postgres,redis,elasticsearch,compose}`), wired
together via Hashicorp go-plugin (gRPC over a Unix socket). The first
four each provision a bough-managed container through the Docker SDK.
`compose` is different by design: it wraps a service in an EXISTING
`docker-compose.yml` and gives it only worktree-scoped port isolation
(see [Compose-wrapped services](#compose-wrapped-services)).

Engines are loaded as gRPC plugins discovered on `PATH`, so a new engine
is a new `bough-plugin-<kind>` binary, not a change to the host.

bough makes no LLM call and has no AI feature. It is git worktrees,
ports, containers, and rendered `.env.local` files — nothing it does
costs anything beyond the machine it runs on.

## Prerequisites

The bough binaries are static Go executables (darwin / linux, arm64 /
amd64). They need:

| Tool | Needed for |
|---|---|
| `git` | every `create` / `remove` (`git worktree`) |
| A Docker-compatible daemon reachable via `DOCKER_HOST` or the platform socket (Docker Desktop / OrbStack / Colima / podman with the docker socket) | every engine |
| `docker compose` v2.24.4 or later | `kind: compose` only (the override uses `!override`) |
| `bash` | `post_create` / `pre_remove` commands |

Docker is the only engine backend; `engines[].backend` may be omitted.
Cold start is the image pull; warm start is 1-5 s.

## Install

bough ships as 6 binaries (`bough` + 5 `bough-plugin-*`). Pick one:

```bash
# 1. GitHub Release tarball (recommended; no Go toolchain needed)
#    Assets are named by Go's GOARCH (amd64/arm64) and embed the release
#    version, so resolve both first.
arch=$(uname -m); case "$arch" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
tag=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/threecorp/bough/releases/latest); tag=${tag##*/}
curl -fsSL "https://github.com/threecorp/bough/releases/download/${tag}/bough_${tag#v}_$(uname -s | tr A-Z a-z)_${arch}.tar.gz" \
  | tar xz -C ~/.local/bin/  bough bough-plugin-mysql bough-plugin-postgres bough-plugin-redis bough-plugin-elasticsearch bough-plugin-compose
#
# macOS (Apple Silicon) one-time step: the release binaries are not
# notarized, so Gatekeeper kills them on first run ("zsh: killed").
# Ad-hoc re-sign locally once after install (and clear quarantine if
# you downloaded via a browser):
#   xattr -d com.apple.quarantine ~/.local/bin/bough ~/.local/bin/bough-plugin-* 2>/dev/null
#   codesign --force --sign - ~/.local/bin/bough ~/.local/bin/bough-plugin-*

# 2. go install (per-binary; requires Go toolchain on PATH)
go install github.com/ikeikeikeike/bough/cmd/bough@latest
go install github.com/ikeikeikeike/bough/cmd/bough-plugin-mysql@latest
go install github.com/ikeikeikeike/bough/cmd/bough-plugin-postgres@latest
go install github.com/ikeikeikeike/bough/cmd/bough-plugin-redis@latest
go install github.com/ikeikeikeike/bough/cmd/bough-plugin-elasticsearch@latest
go install github.com/ikeikeikeike/bough/cmd/bough-plugin-compose@latest

# 3. Nix flake: builds all 6 binaries; `bough --version` names the
#    commit (0.0.0-<rev>) rather than a release tag.
nix profile install github:threecorp/bough
```

Release archives are signed with cosign; [`docs/SIGNING.md`](./docs/SIGNING.md)
shows how to verify one before you extract it.

## Use from Claude Code (plugin)

bough is also packaged as a Claude Code plugin. The `bough` binary still
comes from **Install** above; the plugin's commands shell out to it.

```text
/plugin marketplace add threecorp/bough
claude plugin install bough-all@bough --scope project
```

| Plugin | Ships |
|---|---|
| `bough` | slash commands + the `using-bough` skill |
| `bough-hooks` | the `WorktreeCreate` / `WorktreeRemove` hooks |
| `bough-all` | all of the above |

Install a hook-bearing variant at project scope in the repo you want
`claude --worktree` to work in, and wire hooks one way only — the plugin
or `bough claude hook install`, not both. See
[`docs/PLUGIN_CLAUDE_CODE.md`](./docs/PLUGIN_CLAUDE_CODE.md) for command
names per variant and the CLI equivalents.

## Quick start

Drop a `.bough.yaml` at the monorepo root that declares which sub-repos
hang off `worktrees/<name>/` and which engines start per worktree. Each
repository must already be checked out at the root (or under
`.bough/repos/`), or declare `source:` (a git URL or local path) for bough
to clone:

```yaml
schema_version: 2

monorepo_root: "."

repositories:
  - name: demo-api
    branch_strategy: develop
    direnv: true
    env_local:
      DEMO_API_DSN: "root:@tcp(127.0.0.1:{{ .Mysql.Port }})/demo?parseTime=true"
      DEMO_API_URI: "grpc://0.0.0.0:{{ index .Ports `api` }}"

  - name: demo-dbmigration
    branch_strategy: develop
    direnv: true
    role: engine-provider
    env_local:
      DEMO_DBM_PORT: "{{ .Mysql.Port }}"
    post_create:
      - "make migrate"

engines:
  - kind: mysql           # plugin discovery key (matches bough-plugin-mysql)
    version: "8.4"        # required; image tag fragment — see "Engine versions"
    port_ranges:
      main: [42000, 44999]
    initial_resources:
      - { type: database, name: demo }
    # ready_timeout_sec: 600  # readiness wait after the container starts (plugin default 600)

  # Elasticsearch with engine-managed plugins. bough generates the
  # official elasticsearch-plugins.yml and lets ES install them on boot.
  # Files a plugin needs at runtime (e.g. an analyzer dictionary) mount
  # from a host dir via extras.es.config_mount.
  # - kind: elasticsearch
  #   version: "9.5.3"            # Elastic publishes only full x.y.z tags
  #   port_ranges:
  #     main: [56000, 58999]
  #   extras:
  #     es.mem_limit: "2g"          # docker --memory cap (default: the larger of 2x heap and heap + 1 GiB)
  #     es.config_mount: "demo-api/es-config/analyzer"   # relative to the worktree root
  #   plugins:
  #     - id: analysis-icu          # official plugin: id only
  #     - id: analysis-example      # third-party plugin: id + a direct download URL
  #       location: "https://example.com/analysis-example-{{ .Version }}.zip"

ports:
  api:    { range: [45000, 47999] }

registry:
  path: ".bough/ports.json"
  # backup_dir: "~/.bough/backups"   # copy the registry here before each write; unset = no backup

teardown:
  remove_branch: false     # true also deletes the feature branch on remove
  remove_datadir: true
  # graceful_timeout_sec: 30   # how long remove waits for each engine's Down (unset = the plugin's own default)
```

Each engine takes one host port, from its `main` range. A v0.3
`.worktree-isolation.yaml` is still read (with a warning) when
`.bough.yaml` is absent.

### Engine versions

`engines[].version` is required and is the tag fragment of the engine's
image. Each plugin maps it onto the image its registry publishes and
refuses a shape that registry does not carry at `Up`, naming the YAML key.

| kind | docker image | plugin fallback |
|---|---|---|
| `mysql` | `mysql:<version>` | `8.4` |
| `postgres` | `postgres:<version>-alpine` | `16` |
| `redis` | `redis:<version>-alpine` | `7` |
| `elasticsearch` | `docker.elastic.co/elasticsearch/elasticsearch:<version>` | `9.5.3` |
| `compose` | the wrapped compose file owns the image; `version` is descriptive | — |

`extras.docker.image` sets the image ref verbatim and is the only way
to a variant tag such as `mysql:8.4-oracle`.

Changing the version of a running engine wants a fresh worktree: an
Elasticsearch 7 data directory does not open under 9, and the same holds
across PostgreSQL majors. `bough remove --name <name>` first: while the
worktree's container exists, running or stopped, `bough create` refuses
a `version:` (or `docker.image`) that resolves to a different image and
names both, rather than start the new one on the old data directory.

### Wire it into `claude --worktree`

`bough claude hook install --scope project` writes both hooks into
`.claude/settings.json` (run it from the monorepo root):

```json
{
  "hooks": {
    "WorktreeCreate": [
      {"hooks": [{"type": "command", "command": "bough hook handle --event WorktreeCreate"}]}
    ],
    "WorktreeRemove": [
      {"hooks": [{"type": "command", "command": "bough hook handle --event WorktreeRemove"}]}
    ]
  }
}
```

Wire each event through one command: a hand-added
`bough create --stdin-json` group next to these runs create twice.

After that, `claude --worktree F-FeatureName` (and any other worktree
Claude Code creates: a subagent with `isolation: "worktree"`, or a
background session):

1. Allocates one port per engine plus one per `ports:` entry
2. Materialises every declared sub-repo via `git worktree add`
3. Starts each engine via the matching `bough-plugin-<kind>` plugin
4. Waits for readiness and renders each `.env.local`
5. Runs any per-repo `post_create` commands

A failed sub-repo, template or `post_create` step is printed as a
warning and create still exits 0 once the worktree exists, because
Claude Code needs its path. Pass `--strict` to make those failures fatal.

`bough remove` (or the WorktreeRemove hook) reverses it: plugin Down →
`pre_remove` commands → a check that no engine port still answers → datadir
teardown → `git worktree remove` per sub-repo → registry cleanup. If a port
still answers, remove stops before the datadir step, so the datadir,
worktree and registry entry are kept (Down and `pre_remove` have already
run). A failed `git worktree remove` or branch delete is printed and remove
carries on. The check covers every port the registry holds except those of
a `ports:` entry, so a kind moved from `engines:` to `ports:` under the
same name is no longer checked — stop that engine before removing.

## Workspace layout & resumable worktree sessions

With the default layout, bough's checkouts, registry and worktrees live in two directories:

```
<monorepo-root>/
  .bough/repos/<name>     # source checkouts (bough clones `source:` here)
  .bough/ports.json       # port registry
  worktrees/<name>/       # per-feature worktrees
```

For a fresh layout, a git-initialised root needs two `.gitignore` lines:

```gitignore
.bough/
worktrees/
```

**Why git-init the root?** Claude Code's `--worktree` is git-native. In
a non-git root the `WorktreeCreate` hook still lets `claude --worktree`
run, but Claude Code anchors such sessions to the launch directory, so
`claude --worktree <name> --resume <id>` cannot find them (`claude
--resume <id>` from the root still works). `bough create` warns when the
root is not a git repository; it never edits `.gitignore` for you.

**The container is a work tree of its own.** Inside a git monorepo,
`worktrees/<name>/` would otherwise resolve to the monorepo root, and
Claude Code refuses an isolation worktree that resolves elsewhere. So
`bough create` makes the container a detached worktree of the root. Check
one the way the host does:

```bash
git -C worktrees/<name> rev-parse --show-toplevel   # must print the container's own path
```

`bough claude doctor` names any container a host would refuse, and
`bough repair` (`--dry-run` to preview) converts them in place.

A monorepo still on the pre-v0.11 layout (checkouts at `<root>/<name>`,
worktrees under `.worktrees/`, registry at `.bough-ports.json`) keeps
working; bough reuses the existing locations.

## Compose-wrapped services

`kind: compose` wraps a service in a `docker-compose.yml` you already
have — no second source of truth for the image — and adds only
deterministic, worktree-scoped port isolation.

```yaml
engines:
  - kind: compose
    version: "7-alpine"       # descriptive only; the compose file owns the real version
    port_ranges:
      main: [59000, 59999]    # HOST port range bough allocates from
    compose:
      file: "demo-api/compose.yml"  # relative to the monorepo worktree root
      service: "redis"              # the service bough starts and publishes
      target_port: 6379             # the CONTAINER-side port the service listens on
      # project: ""                 # optional; default "bough-<worktree>-<file>"
      # env_prefix: ""              # optional; default upper(service) → BOUGH_REDIS_*
```

bough never edits your compose file. It renders a worktree-scoped
override (fixed host port + a `bough-compose-<port>` container name) and
runs `docker compose -f demo-api/compose.yml -f <override> -p
<worktree-scoped-project> up -d redis`. Each worktree gets its own project
name, container name and host port for that service, so two worktrees
using the same file do not collide on them. Two caveats: the project name
lowercases the worktree name and folds other characters to `-`
(`F_Foo` and `F-Foo` map to the same project), and a `compose.project` you
set is used as is.

Trade-offs versus the four native plugins:

- **Compose starts the service's dependencies too** (`depends_on`), as
  `docker compose up <service>` always does. Only the wrapped service gets
  bough's port and name override, and remove stops only that service:
  dependencies and the project network stay up until you remove them.
- **`teardown.remove_datadir: true` does not touch compose-managed
  volumes.** `Down` stops and removes the container; the data in your
  compose file's volumes is yours to delete.
- **`ReadyCheck` defaults to a plain TCP dial.** Set
  `extras: {compose.ready_probe: "redis"}` (or `postgres` / `mysql` /
  `http`) for a protocol-level check. Set it when wrapping mysql: a TCP
  dial succeeds during the image's first-run bootstrap, before the real
  server is up.
- **One compose entry per `.bough.yaml`.** Engine kinds must be unique,
  so a second service from the same file cannot be wrapped today.

## CLI surface

```
# Worktree isolation
bough create [--name NAME] [--cwd PATH] [--stdin-json] [--strict] [--no-fetch] [--config PATH]
bough remove [--name NAME | --path PATH] [--stdin-json] [--graceful-timeout SEC] [--config PATH]
bough verify <worktree-name>            # registry ports vs declared ranges, and .env.local present
bough status [--json]                   # registered ports + whether each is listening
bough list                              # registry table (one column per port kind)
bough backfill                          # register pre-existing worktrees/* by name
bough repair [--dry-run]                # convert containers a Claude Code host would refuse
bough config validate [PATH]            # strict YAML schema check (default: ./.bough.yaml)
bough plugins list                      # bough-plugin-* binaries found on PATH

# What bough installs into Claude Code (--scope project = ./.claude, user = ~/.claude)
bough claude hook install | uninstall | list | replay | doctor
bough claude skill install | uninstall | list
bough claude command install | uninstall | list
bough claude doctor                     # hook wiring, containers, engine plugins, retired leftovers
```

`claude --worktree` reaches create / remove through
`bough hook handle --event WorktreeCreate|WorktreeRemove`, which the hooks
wire for you. `bough hook` and `bough doctor` still work as deprecated
aliases of their `bough claude ...` homes.

## Status

Three of the four native engine plugins
(`bough-plugin-{mysql,redis,elasticsearch}`) are used in a real Go +
Rails + Remix multi-sub-repo monorepo (MySQL 8.4 LTS + Redis 7 +
Elasticsearch). `bough-plugin-postgres` and `bough-plugin-compose` are
covered by the conformance suite and integration tests only.

The plugin contract models multi-port engines (rabbitmq AMQP+management,
kafka broker+controller), but the host allocates and passes only the
`main` port today, and no multi-port plugin is bundled.

Upgrading from v0.27.0 or earlier: see
[`docs/MIGRATION-v0.27-to-v0.28.md`](./docs/MIGRATION-v0.27-to-v0.28.md).
What is planned next is in [`docs/ROADMAP.md`](./docs/ROADMAP.md); the
release-by-release history is in [`CHANGELOG.md`](./CHANGELOG.md).

## Plugin conformance

Every PR's CI runs the [`bough/conformance`](./conformance) suite
against a real Docker container, one (runner × plugin) cell at a
time. Plugin authors (internal or third-party) verify their contract
with one test function:

```go
//go:build conformance
func TestMyPluginConformance(t *testing.T) {
    conformance.Run(t, conformance.Config{
        PluginBinary: os.Getenv("BOUGH_CONFORMANCE_PLUGIN_BIN"),
        Image:        "myengine:1.0",
    })
}
```

Locally:

```bash
make build
make conformance-local PLUGIN=mysql       # one plugin
make conformance-all                       # all five
```

See [`docs/PLUGIN_AUTHOR_GUIDE.md`](./docs/PLUGIN_AUTHOR_GUIDE.md)
for the walkthrough, [`plugins/engine/api/CONTRACT.md`](./plugins/engine/api/CONTRACT.md)
for the prose contract, and
[`examples/plugin-template/`](./examples/plugin-template) for a
conformance-test starting point.

## Contributing

Bug reports and pull requests welcome — please run `make test`,
`make lint`, and `make build` locally before opening a PR. For
plugin work also run `make conformance-local PLUGIN=<kind>` (needs
Docker).

## License

MIT. See `LICENSE` for the full text.
