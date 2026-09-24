# bough

> Per-worktree isolation orchestrator for monorepos.

`bough` brings up an isolated dev environment per git worktree (per
feature branch): a deterministically-allocated port set, an
auto-generated `.env.local` in every sub-repo, and a worktree-local
instance of every declared engine — all driven by one `.bough.yaml`
at the monorepo root.

bough itself is a small Go CLI plus five engine plugins
(`bough-plugin-{mysql,postgres,redis,elasticsearch,compose}`), wired
together via Hashicorp go-plugin (gRPC over Unix socket). Each of the
first four provisions a bough-managed container through the Docker SDK.
The lifecycle (`up` / `ready check` / `down`) sits behind a per-plugin
backend seam, so a second runtime is an implementation a plugin
registers rather than a change to the host — once `Down` and `ReadyCheck`
carry the backend token on the wire, which they do not yet. `compose` is different by
design — instead of provisioning its own engine, it wraps an EXISTING
`docker-compose.yml`/service an operator already has, giving it only
worktree-scoped port isolation (see [Compose-wrapped
services](#compose-wrapped-services) below).

The "what to isolate" is fully declarative — pick which repositories
appear under `worktrees/<name>/` and which engines spawn per worktree
via a single YAML at the monorepo root. Engines are loaded as gRPC
plugins, so adding a new engine (rabbitmq, kafka, nats, minio, …) never
requires editing the host binary.

bough makes no LLM call and has no AI feature. It is git worktrees,
ports, containers, and rendered `.env.local` files — nothing it does
costs anything beyond the machine it runs on.

## Prerequisites

bough binaries themselves are static Go executables (darwin / linux,
arm64 / amd64) — `bough` never installs Docker for you. What it needs
is one thing:

| Backend  | User must provide |
|----------|-------------------|
| `docker` | A Docker-compatible daemon reachable via `DOCKER_HOST` or the platform socket (Docker Desktop / OrbStack / Colima / podman with the docker socket) |

`docker` is the only backend the bundled plugins register, and the one
an engine gets when `.bough.yaml` leaves `engines[].backend` out — the
host does not probe for one. Cold start is the image pull; warm start
is 1-5 s. (v0.1-v0.26 also shipped a Nix / services-flake backend; it
was removed in v0.27.0 — see the CHANGELOG.)

## Install

bough ships as 6 binaries (`bough` + 5 `bough-plugin-*`). Pick one:

```bash
# 1. GitHub Release tarball (recommended; no Go / Nix toolchain needed)
#    Available for darwin/linux × arm64/amd64. Two translations are
#    needed to build the asset URL: `uname -m` reports x86_64/aarch64
#    but assets are named by Go's GOARCH (amd64/arm64), and the asset
#    filename embeds the release version (bough_<ver>_<os>_<arch>) so
#    the tag is resolved first via the /releases/latest redirect.
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

# 3. Nix flake (requires Nix with flakes enabled)
nix run    github:threecorp/bough -- create --stdin-json
nix profile install github:threecorp/bough

# 4. Homebrew (planned — tap not yet published)
# brew tap     threecorp/tap
# brew install bough
```

## Use from Claude Code (plugin)

bough is also packaged as a Claude Code plugin, so you can drive it from inside a
session instead of dropping to a shell. The `bough` binary itself still comes
from **Install** above (install it on `PATH` first; the plugin's commands shell
out to it).

```text
/plugin marketplace add threecorp/bough
/plugin install bough-all@bough
```

Three variants share one tree — pick by what you want acting on your sessions:

| Plugin | Ships | Install when |
|---|---|---|
| `bough` | commands + skill | You want `/bough:*` on hand. Inert until invoked, so it is safe at any scope. |
| `bough-hooks` | hooks | You drive bough from the shell and only want `claude --worktree` wired. |
| `bough-all` | commands + skill + hooks | You want the lot in one line. |

Installing wires the user-facing surface:

- **Slash commands** — type them in any session: `/bough:create <name>`,
  `/bough:remove <name>`, `/bough:list`, `/bough:status`, `/bough:verify <name>`,
  `/bough:doctor`, `/bough:config-validate`. Each one shells out to `bough` and
  summarises the result.
- **Skill** — `using-bough`, model-invoked guidance on which `/bough:*` fits an
  intent, with a `command -v bough` PATH preflight.

Commands and the skill are inert until invoked — nothing happens until you type
one — so the `bough` variant is side-effect-free at any scope.

**Hooks are the part to scope deliberately.** They carry `WorktreeCreate` and
`WorktreeRemove`, and they act on whichever monorepo the session is started in,
so install a hook-bearing variant into the repo you actually want
`claude --worktree` to work in:

```text
claude plugin install bough-all@bough --scope project   # this repo only  (recommended)
claude plugin install bough-all@bough --scope user      # every repo on the machine
```

Project scope writes `enabledPlugins` into that repo's `.claude/settings.json`
and the hooks fire only in sessions started there. The `claude plugin install`
CLI defaults to user scope when `--scope` is omitted, so pass it.

Prefer no plugin? The CLI installs the same artifacts from the binary's embedded
copy — `bough claude hook|skill|command install --scope project`. Wire hooks one
way or the other, not both: they run the same dispatcher, so keeping both fires
every event twice (`bough claude doctor` flags it).

See [`docs/PLUGIN_CLAUDE_CODE.md`](./docs/PLUGIN_CLAUDE_CODE.md) for the variant
layout, the full command / hook reference, and the CLI equivalents.

## Quick start

Drop a `.bough.yaml` at the monorepo root that declares which sub-repos
hang off `worktrees/<name>/` and which engines start per worktree
(v0.3.x `.worktree-isolation.yaml` is auto-read with a deprecation
warning — see [`docs/MIGRATION-v0.3-to-v0.4.md`](./docs/MIGRATION-v0.3-to-v0.4.md)):

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
      # Use whichever shell / toolchain your repo standardises on —
      # bough only runs the command, it does not assume Nix here.
      - "make migrate"

engines:
  - kind: mysql           # plugin discovery key (matches bough-plugin-mysql)
    version: "8.4"        # image tag fragment — see "Engine versions" below
    port_ranges:
      main: [42000, 44999]
    initial_resources:
      - { type: database, name: demo }
    # backend: docker     # optional; docker is the only bundled backend and the default
    # ready_timeout_sec: 600  # default 600s; covers the first image pull

  # Multi-port engine example — plugin lands in v0.5+; schema is ready in v0.4.
  # - kind: rabbitmq
  #   version: "3-management"
  #   port_ranges:
  #     amqp:       [60000, 60499]
  #     management: [60500, 60999]
  #   initial_resources:
  #     - { type: vhost, name: dev }

  # Wrap an EXISTING docker-compose.yml instead of provisioning a new
  # engine — see "Compose-wrapped services" below.
  # - kind: compose
  #   version: "7-alpine"      # descriptive only; the real version lives in the compose file
  #   port_ranges:
  #     main: [59000, 59999]
  #   compose:
  #     file: "demo-api/compose.yml"   # relative to the monorepo worktree root
  #     service: "redis"
  #     target_port: 6379

  # Elasticsearch with engine-managed plugins (docker backend). bough
  # generates the official elasticsearch-plugins.yml and lets ES install
  # them idempotently on boot — no custom entrypoint. Auxiliary plugin
  # files a plugin needs at runtime (e.g. an analyzer dictionary) mount
  # from a host dir via extras.es.config_mount.
  # - kind: elasticsearch
  #   version: "9.5.3"            # Elastic publishes only full x.y.z tags
  #   port_ranges:
  #     main: [56000, 58999]
  #   extras:
  #     es.mem_limit: "2g"          # docker --memory cap (default: 2x heap) — guards the host VM from OOM
  #     es.config_mount: "demo-api/es-config/analyzer"   # host dir, relative to the monorepo worktree root
  #   plugins:
  #     - id: analysis-icu          # official plugin: id only
  #     - id: analysis-example      # third-party plugin: id + a direct download URL
  #       location: "https://example.com/analysis-example-{{ .Version }}.zip"

ports:
  api:    { range: [45000, 47999] }

registry:
  path: ".bough/ports.json"   # pre-v0.11 monorepos may keep ".bough-ports.json"
  backup_dir: "~/.bough/backups"

teardown:
  remove_branch: true
  remove_datadir: true
  graceful_timeout_sec: 10
```

### Engine versions

`engines[].version` is the tag fragment of the engine's image. Each
plugin maps it onto the image its registry publishes and refuses a
shape that registry does not carry at `Up` — naming the YAML key and
the escape hatch — rather than failing later at the pull.

| kind | docker image | default |
|---|---|---|
| `mysql` | `mysql:<version>` | `8.4` |
| `postgres` | `postgres:<version>-alpine` | `16` |
| `redis` | `redis:<version>-alpine` | `7` |
| `elasticsearch` | `docker.elastic.co/elasticsearch/elasticsearch:<version>` | `9.5.3` |
| `compose` | — the wrapped compose file owns the image | — |

`extras.docker.image` sets the image ref verbatim and is the only way
to a variant tag such as `mysql:8.4-oracle`.

Changing the version of a running engine wants a fresh worktree: an
Elasticsearch 7 data directory does not open under 9, and the same holds
across PostgreSQL majors. So `bough remove` the worktree first — a
still-running container is reused by name, so a new `version:` is not
picked up until that container is gone, and its `.local/<kind>-data` is
removed with it.

Then wire it into Claude Code's `WorktreeCreate` / `WorktreeRemove`
hooks in `.claude/settings.json`. `bough claude hook install` writes
both for you, routed through the single `bough hook handle` dispatcher:

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

> Wire each event through **one** command. `bough hook handle --event
> WorktreeCreate` and the older `bough create --stdin-json` both run the
> full create pipeline, so keeping both for the same event runs it twice
> (a second `post_create` migration pass). `bough claude hook install` only
> ever manages its own `bough hook handle` entries, so prefer it and don't
> also hand-add a `bough create --stdin-json` group.

After that, `claude --worktree F-FeatureName` deterministically:

1. Allocates a port set (one per declared engine role + per
   `ports:` kind) for the branch
2. Materialises every declared sub-repo via `git worktree add`
3. Spawns each configured engine via the matching
   `bough-plugin-<kind>` gRPC plugin
4. Polls for readiness and renders each `.env.local` template
5. Runs any per-repo `post_create` hooks (migrations, seed-force, etc.)

`bough remove` (or the WorktreeRemove hook) reverses all of the above:
graceful plugin Down → lsof PID kill fallback → `git worktree remove`
per sub-repo → registry cleanup → datadir teardown.

## Workspace layout & resumable worktree sessions

Everything bough generates at the monorepo root is grouped under two
directories (v0.11+):

```
<monorepo-root>/
  .bough/repos/<name>     # source checkouts (bough clones `source:` here)
  .bough/ports.json       # port registry
  worktrees/<name>/       # per-feature worktrees
```

So a git-initialised monorepo root needs just two `.gitignore` entries:

```gitignore
.bough/
worktrees/
```

**Why git-init the root?** Claude Code's `--worktree` is git-native. In
a non-git root, bough's `WorktreeCreate` hook still lets `claude
--worktree` run (the hook is Claude Code's documented escape hatch for
non-git / other-VCS workspaces), **but** Claude Code anchors such
hook-based worktree sessions to the launch directory. That means:

- ✅ `claude --resume <id>` from the monorepo root — always works.
- ❌ `claude --worktree <name> --resume <id>` — cannot find the session
  (it looks in the worktree's own project bucket, where a non-git
  hook-based session was never stored).

`git init` the monorepo root and Claude Code switches onto the
git-native path, making `claude --worktree <name> --resume <id>` work
too. `bough create` prints a one-time heads-up (with the two `.gitignore`
lines above) when it notices the root is not a git repository — it never
edits `.gitignore` for you.

**The container is a work tree of its own.** `worktrees/<name>/` holds
one sub-repo worktree per repository and carries no commits itself, so it
used to be an ordinary directory. Inside a git monorepo that is not
enough: git discovery walks up and resolves the container to the monorepo
root, and Claude Code refuses an isolation worktree whose working tree
resolves elsewhere — commands run there would write outside it. So when
the root is a repo, `bough create` materialises the container as a
**detached** worktree of it (no branch, nothing to clean up later). You
can check any container the way the host does:

```bash
# must print the container's own path, not the monorepo root
git -C worktrees/<name> rev-parse --show-toplevel
```

`bough doctor` reports this for every container it finds, and names the
ones a host would refuse. Containers created before this landed stay plain
directories — `git worktree add` cannot adopt a populated directory —
so recreate one (`bough remove <name>` && `bough create <name>`) to fix
it, or start the session with `cd worktrees/<name> && claude`.

> **Upgrading from a pre-v0.11 layout is transparent.** A monorepo whose
> checkouts still sit at `<root>/<name>` and whose worktrees still live
> under `.worktrees/` keeps working unchanged — bough detects and reuses
> the existing locations, and only fresh checkouts / worktrees adopt the
> new paths. To fully consolidate, move the sub-repo checkouts into
> `.bough/repos/`, rename `.worktrees/` → `worktrees/`, and (optionally)
> `.bough-ports.json` → `.bough/ports.json`.

## Compose-wrapped services

The four bundled engines above are ones bough fully provisions itself
(a bough-managed Docker container). `kind: compose` is different: it
wraps a `docker-compose.yml` you already have — no second source of
truth for the image/version — and gives it only the one thing bough is
actually good at:
deterministic, worktree-scoped port isolation.

```yaml
engines:
  - kind: compose
    version: "7-alpine"       # descriptive only; the compose file owns the real version
    port_ranges:
      main: [59000, 59999]    # HOST port range bough allocates from
    compose:
      file: "demo-api/compose.yml"  # relative to the monorepo worktree root
      service: "redis"              # only this service is touched — siblings in
                                     # the same file are left alone
      target_port: 6379             # the CONTAINER-side port the service listens on
      # project: ""                 # optional; default "bough-<worktree>-<file>"
      # env_prefix: ""              # optional; default upper(service) → BOUGH_REDIS_*
```

What bough does under the hood, without ever editing your compose
file: it renders a small worktree-scoped override (fixed host port +
a `bough-compose-<port>` container name) and runs `docker compose -f
demo-api/compose.yml -f <override> -p <worktree-scoped-project> up -d
redis`. Two worktrees pointing at the textually-identical file never
collide — different project, different container, different port.

Trade-offs versus the four native plugins, by design:

- **`teardown.remove_datadir: true` does not touch compose-managed
  volumes.** `Down` stops and removes the container; deleting the
  data your compose file's own volumes hold is left to you, since
  bough does not own that lifecycle here.
- **`ReadyCheck` defaults to a plain TCP dial**, not a protocol-level
  handshake (unlike the native plugins' mysql/redis/postgres/HTTP
  probes). Set `extras: {compose.ready_probe: "redis"}` (or
  `postgres` / `mysql` / `http`) on the engine entry if you want a
  real protocol check instead. **Set this explicitly when wrapping
  mysql** — a bare TCP dial goes ready during the mysql image's
  first-run "temporary server" bootstrap phase, the same race the
  bundled mysql plugin's own Docker backend had to fix; `mysql`
  reads the server's handshake packet instead of just dialing.
- **One compose service per `Engine` entry.** Wrapping two services
  from the same compose file needs two separate `kind: compose`
  engine entries pointing at the same `file` with different
  `service`/`target_port` values — bough tears down each
  independently but never removes the file's shared network, so it
  is left behind as harmless cruft after the last one exits.

## CLI surface

```
# Worktree isolation
bough create [--config PATH] [--name NAME] [--stdin-json] [--cwd PATH]
bough remove [--config PATH] [--name NAME | --path PATH] [--stdin-json]
bough verify <worktree-name>            # registry vs declared ranges vs .env.local
bough status [--json]                   # registry + lsof TCP listen probe
bough list                              # registry table (kinds dynamic)
bough backfill                          # register pre-existing worktrees/* (or legacy .worktrees/*)
bough config validate [PATH]            # strict YAML schema check
bough plugins list                      # glob $PATH for bough-plugin-*

# What bough installs into Claude Code
bough claude hook install | uninstall | list     # hook wiring in .claude/settings.json
bough claude skill install | uninstall | list    # the using-bough skill
bough claude command install | uninstall | list  # the /bough:* commands
bough claude doctor                              # hook wiring + engine plugins + retired leftovers
```

`claude --worktree` reaches `bough create` / `bough remove` through
`bough hook handle --event WorktreeCreate|WorktreeRemove`, which is wired for
you and not typed by hand. `bough hook` / `bough doctor` still work as
deprecated aliases of their `bough claude ...` homes for the v0.x line.

## Repository layout

```
bough/
├── cmd/
│   ├── bough/                              host CLI entrypoint
│   ├── bough-plugin-mysql/                 MySQL plugin entrypoint
│   ├── bough-plugin-postgres/              PostgreSQL plugin entrypoint
│   ├── bough-plugin-redis/                 Redis plugin entrypoint
│   ├── bough-plugin-elasticsearch/         Elasticsearch plugin entrypoint
│   └── bough-plugin-compose/               Compose-wrapper plugin entrypoint
├── internal/                              # worktree isolation core
│   ├── cli/                                cobra subcommands
│   ├── config/                             .bough.yaml schema (validator/v10)
│   ├── allocator/                          crc32 + linear-probing port allocator
│   ├── registry/                           .bough/ports.json atomic R/W (legacy .bough-ports.json read fallback)
│   ├── gitwt/                              `git worktree` wrapper
│   ├── envwriter/                          text/template + Sprig .env.local generator
│   ├── hooks/                              Claude Code hook wiring in .claude/settings.json (install / list / replay / doctor)
│   ├── pluginhost/                         go-plugin discovery + lifecycle
│   └── pluginsign/                         plugin binary signature verification
├── plugins/
│   └── engine/
│       ├── api/                            gRPC EngineProvider contract + Go interface
│       ├── mysql/                          MySQL provider (Docker SDK)
│       ├── postgres/                       PostgreSQL provider (Docker SDK)
│       ├── redis/                          Redis provider (Docker SDK)
│       ├── elasticsearch/                  Elasticsearch provider (Docker SDK)
│       └── compose/                        Wraps an existing docker-compose.yml/service
├── tests/
│   └── integration/                        real-services E2E (build tag: integration)
├── flake.nix                               devShells.ci / devShells.default
├── .goreleaser.yaml                        cross-compile + GitHub Release
└── .github/workflows/                      ci.yml + release.yml
```

## Roadmap

| Milestone | Headline                                                                                    |
|-----------|---------------------------------------------------------------------------------------------|
| v0.1.0-α  | Nix `services-flake` backend, 4 DB plugins (mysql / postgres / redis / elasticsearch)        |
| v0.1.1    | Bundled `flake.lock` per plugin (cold start 5-10 min → 30-60 s), `packages.default` for `nix run` / `nix profile install`, per-engine `ready_timeout_sec` config, honest README |
| v0.2.0    | Docker backend, hybrid `backend:` selector — explicit `nix` / `docker` in YAML, or auto-detect (Nix-with-flakes present → Nix, else Docker daemon → Docker, else clear error) when the field is omitted (the Nix half was removed in v0.27.0) |
| v0.3.0    | Plugin conformance suite + CI matrix on real Docker — plugin authors verify their contract end-to-end with one test func, four bough-internal plugins are gated on `ubuntu-24.04` + `ubuntu-24.04-arm` × `mysql` / `postgres` / `redis` / `elasticsearch` |
| v0.4.0    | Generic engine plugin orchestrator (was: DB-only). `DBProvider` → `EngineProvider`, `plugins/db/` → `plugins/engine/`, YAML schema v2 (`.bough.yaml` / `engines:` / `port_ranges:` per role / `initial_resources:`). Multi-port engines (rabbitmq AMQP+Management, kafka broker+controller, NATS client+monitor+cluster) are first-class; v0.4.x reads every v0.3 surface with a deprecation warning — only the plugin gRPC handshake (`DBProvider`/`BOUGH_DB_PLUGIN`) was removed in v0.5.0, the YAML-level fallback (old file name / section / field names) is still read today, see [docs/MIGRATION-v0.3-to-v0.4.md](docs/MIGRATION-v0.3-to-v0.4.md) |
| v0.22.0   | `claude --worktree` works against a git monorepo again: the worktree container is a work tree of its own (checked out at an empty tree, so it still starts empty), `bough doctor` names any container a host would refuse, and the release pipeline runs the published archive through the real WorktreeCreate/Remove hook contract before the release is called good |
| v0.9.0-v0.26.0 | (retired) A continuous-learning loop layered on top of the isolation core: an on-disk instinct corpus, `claude --print` clustering into skills / agents / commands, and six extra Claude Code hook events. (v0.5.0-v0.8.0 carried a different, superseded memory-orchestration surface — see [docs/attic/](docs/attic/).) Removed wholesale in v0.27.0 — pin v0.26.0 if you depend on it, and see [docs/MIGRATION-v0.26-to-v0.27.md](docs/MIGRATION-v0.26-to-v0.27.md) |
| v0.27.0   | bough is an isolation tool and nothing else: the continuous-learning loop above is gone, two hook events are wired instead of eight, and the retired `.bough.yaml` sections are read with a warning for one minor series. Also: Docker is the only engine backend. The Nix / services-flake path is gone (it could not start Elasticsearch at all, gave Postgres different credentials than the container does, and no CI job had ever run it); `engines[].backend` accepts only `docker` and may be omitted. Each plugin now registers its backend in `New()`, so a second runtime is an implementation rather than another branch (it also needs the backend token on `Down` / `ReadyCheck`) |
| next      | Reference rabbitmq / kafka / NATS / minio engine plugins, Homebrew tap |

[embedded-postgres]: https://github.com/fergusstrange/embedded-postgres

## Status

Three of the four bundled engine plugins
(`bough-plugin-{mysql,redis,elasticsearch}`) are battle-tested in a
real Go + Rails + Remix multi-sub-repo monorepo (MySQL 8.4 LTS +
Redis 7 + Elasticsearch) on the Docker backend — the only backend
since v0.27.0. The Postgres plugin
(`bough-plugin-postgres`) is integration-test-only — it has not run in
that production monorepo. Multi-port engines (rabbitmq / kafka / NATS) are
first-class in the contract — reference plugins are not yet bundled.

The worktree-isolation core has been stable since v0.4.0. v0.9.0 through
v0.26.0 also carried a continuous-learning loop on top of it; v0.27.0
removed that wholesale and bough is an isolation tool again. Pin
**v0.26.0** to keep the loop, or see
[docs/MIGRATION-v0.26-to-v0.27.md](docs/MIGRATION-v0.26-to-v0.27.md).

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
for the prose contract every assertion traces back to, and
[`examples/plugin-template/`](./examples/plugin-template) for a
copy-this skeleton with TODO markers.

## Contributing

Bug reports and pull requests welcome — please run `make test`,
`make lint`, and `make build` locally before opening a PR. For
plugin work also run `make conformance-local PLUGIN=<kind>` (needs
Docker).

## License

MIT. See `LICENSE` for the full text.
