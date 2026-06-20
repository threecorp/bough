# bough

> Per-worktree isolation orchestrator for monorepos.

`bough` brings up an isolated dev environment per git worktree (per
feature branch): a deterministically-allocated port set, an
auto-generated `.env.local` in every sub-repo, and a worktree-local
instance of every declared engine — all driven by one `.bough.yaml`
at the monorepo root.

bough itself is a small Go CLI plus four engine plugins
(`bough-plugin-{mysql,postgres,redis,elasticsearch}`), wired together
via Hashicorp go-plugin (gRPC over Unix socket). Each plugin defers
the actual lifecycle (`up` / `ready check` / `down`) to a backend you
choose: today that's [services-flake][services-flake] on top of Nix
or a direct Docker SDK backend; the host's auto-detect picks one
based on what's on the runner.

The "what to isolate" is fully declarative — pick which repositories
appear under `.worktrees/<name>/` and which engines spawn per worktree
via a single YAML at the monorepo root. Engines are loaded as gRPC
plugins, so adding a new engine (rabbitmq, kafka, nats, minio, …) never
requires editing the host binary.

[services-flake]: https://github.com/juspay/services-flake

## Prerequisites

bough binaries themselves are static Go executables (darwin / linux,
arm64 / amd64) — `bough` never installs Nix or Docker for you. The
host auto-detects which backend each plugin uses with a v0.1.x-compat
preference for `nix` (so monorepos that adopted bough when nix was
the only option do not silently flip to docker on upgrade):

  1. nix-with-flakes on `PATH` → `nix`
  2. else docker daemon reachable → `docker`
  3. else: actionable error pointing at the `engines[].backend` YAML knob

In practice that means **nix users** (= those who already installed
Nix before adopting bough) get `nix`; **everyone else (the typical
install)** gets `docker`. An explicit `backend: nix | docker` per
engine in `.bough.yaml` always overrides auto-detect.

| Backend  | When auto-detect picks it                            | User must provide |
|----------|------------------------------------------------------|-------------------|
| `docker` | nix-with-flakes not on PATH, docker daemon reachable (= typical install) | A Docker-compatible daemon (Docker Desktop / OrbStack / Colima / podman with the docker socket) |
| `nix`    | nix-with-flakes on PATH                              | Nix with flakes enabled + network access to flakehub.com / github.com on first invocation |

Cold-start cost (first `bough create` invocation on a fresh machine):

| Backend                                                | Cold start          | Warm start  |
|--------------------------------------------------------|---------------------|-------------|
| `nix` (v0.1.0, no bundled `flake.lock`; historical)    | 5-10 min ⚠         | 10-60 s     |
| `nix` (v0.1.1+, bundled `flake.lock`)                  | 30-60 s             | 5-10 s      |
| `docker` (v0.2+, after image pull)                     | image pull dominant | 1-5 s       |

v0.1.1 added the bundled `flake.lock` per plugin (no more flakehub.com
round-trip on every fresh worktree); v0.2 added the Docker backend so
users who prefer Docker over Nix can avoid Nix entirely.

## Install

bough ships as 5 binaries (`bough` + 4 `bough-plugin-*`). Pick one:

```bash
# 1. GitHub Release tarball (recommended; no Go / Nix toolchain needed)
#    Available for darwin/linux × arm64/amd64. The URL resolves the
#    latest tagged release automatically.
curl -fsSL https://github.com/ikeikeikeike/bough/releases/latest/download/bough_$(uname -s | tr A-Z a-z)_$(uname -m).tar.gz \
  | tar xz -C ~/.local/bin/  bough bough-plugin-mysql bough-plugin-postgres bough-plugin-redis bough-plugin-elasticsearch

# 2. go install (per-binary; requires Go toolchain on PATH)
go install github.com/ikeikeikeike/bough/cmd/bough@latest
go install github.com/ikeikeikeike/bough/cmd/bough-plugin-mysql@latest
go install github.com/ikeikeikeike/bough/cmd/bough-plugin-postgres@latest
go install github.com/ikeikeikeike/bough/cmd/bough-plugin-redis@latest
go install github.com/ikeikeikeike/bough/cmd/bough-plugin-elasticsearch@latest

# 3. Nix flake (requires Nix with flakes enabled)
nix run    github:ikeikeikeike/bough -- create --stdin-json
nix profile install github:ikeikeikeike/bough

# 4. Homebrew (planned — tap not yet published)
# brew tap     ikeikeikeike/tap
# brew install bough
```

## Quick start

Drop a `.bough.yaml` at the monorepo root that declares which sub-repos
hang off `.worktrees/<name>/` and which engines start per worktree
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
    version: "8.4"
    port_ranges:
      main: [42000, 44999]
    socket_dir: "/tmp"
    initial_resources:
      - { type: database, name: demo }
    # backend: nix        # optional; auto-detects nix-with-flakes / docker when omitted
    # ready_timeout_sec: 600  # v0.1.1+; default 600s for nix cold paths

  # Multi-port engine example — plugin lands in v0.5+; schema is ready in v0.4.
  # - kind: rabbitmq
  #   version: "3-management"
  #   port_ranges:
  #     amqp:       [60000, 60499]
  #     management: [60500, 60999]
  #   initial_resources:
  #     - { type: vhost, name: dev }

ports:
  api:    { range: [45000, 47999] }

registry:
  path: ".bough-ports.json"
  backup_dir: "~/.bough/backups"

teardown:
  remove_branch: true
  remove_datadir: true
  graceful_timeout_sec: 10
```

Then wire it into Claude Code's `WorktreeCreate` / `WorktreeRemove`
hooks in `.claude/settings.json`:

```json
{
  "hooks": {
    "WorktreeCreate": [
      {"hooks": [{"type": "command", "command": "bough create --stdin-json"}]}
    ],
    "WorktreeRemove": [
      {"hooks": [{"type": "command", "command": "bough remove --stdin-json"}]}
    ]
  }
}
```

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

## CLI surface

```
bough create [--config PATH] [--name NAME] [--stdin-json] [--cwd PATH]
bough remove [--config PATH] [--name NAME | --path PATH] [--stdin-json]
bough verify <worktree-name>            # registry vs declared ranges vs .env.local
bough status [--json]                   # registry + lsof TCP listen probe
bough list                              # registry table (kinds dynamic)
bough backfill                          # register pre-existing .worktrees/*
bough config validate [PATH]            # strict YAML schema check
bough plugins list                      # glob $PATH for bough-plugin-*
```

## Repository layout

```
bough/
├── cmd/
│   ├── bough/                              host CLI entrypoint
│   ├── bough-plugin-mysql/                 MySQL plugin entrypoint
│   ├── bough-plugin-postgres/              PostgreSQL plugin entrypoint
│   ├── bough-plugin-redis/                 Redis plugin entrypoint
│   └── bough-plugin-elasticsearch/         Elasticsearch plugin entrypoint
├── internal/
│   ├── cli/                                cobra subcommands
│   ├── config/                             YAML schema (validator/v10)
│   ├── allocator/                          crc32 + linear-probing port allocator
│   ├── registry/                           .bough-ports.json atomic R/W (legacy .worktree-ports.json read on fallback)
│   ├── gitwt/                              `git worktree` wrapper
│   ├── envwriter/                          text/template + Sprig .env.local generator
│   ├── hooks/                              post_create / pre_remove hook runner
│   ├── mcp/                                ~/.claude.json projects upsert
│   └── pluginhost/                         go-plugin discovery + lifecycle
├── plugins/
│   └── engine/
│       ├── api/                            gRPC EngineProvider contract + Go interface
│       ├── mysql/                          MySQL 8.4 provider + embedded services-flake
│       ├── postgres/                       PostgreSQL 16 provider + embedded services-flake
│       ├── redis/                          Redis 7 provider + embedded services-flake
│       └── elasticsearch/                  Elasticsearch 7 provider + process-compose-flake
├── tests/
│   └── integration/                        real-services E2E (build tag: integration)
├── flake.nix                               devShells.ci / devShells.default
├── .goreleaser.yaml                        cross-compile + GitHub Release
└── .github/workflows/                      ci.yml + release.yml
```

## Instincts (v0.5+)

> **bough is not an agent memory system. bough is a per-worktree memory orchestration layer.**

v0.5 adds an opt-in instinct subsystem: behavioural rules and
observations the host accumulates per worktree, repo, and global
scope. Memory intelligence is delegated to external OSS backends
(mem0 / Graphiti / Letta, v0.6+); bough provides the canonical
schemas, scope model, safety pipeline (redaction, poisoning
guard, dedupe, decay), and the conformance contract every backend
honours.

The subsystem is **off by default**. Enable it by setting
`instinct.enabled: true` in `.bough.yaml`:

```yaml
instinct:
  enabled: true
  default_memory_backend: sqlite

memory_backends:
  - kind: sqlite
    role: reference-fallback
    path: ".bough/memory/reference.db"
    fts: true
    wal: true
```

The PRIMARY ingest path is stdin:

```sh
make test 2>&1 | bough instinct ingest --stdin --source test_failure
bough instinct query --term "early returns"
bough instinct approve <id>
```

The Claude `.jsonl` file watch observer is opt-in beta because of
fsnotify cross-platform fragility (macOS FSEvents vs Linux inotify
divergence, log rotation, truncate).

See [docs/INSTINCTS.md](docs/INSTINCTS.md) for the full lifecycle,
[docs/BACKENDS.md](docs/BACKENDS.md) for choosing a backend,
[docs/EXTERNAL_MEMORY_BACKENDS.md](docs/EXTERNAL_MEMORY_BACKENDS.md)
for wiring mem0 / Graphiti, [docs/SECURITY.md](docs/SECURITY.md)
for plugin trust, and [docs/ROADMAP.md](docs/ROADMAP.md) for
v0.6 / v0.7+.

## Roadmap

| Milestone | Headline                                                                                    |
|-----------|---------------------------------------------------------------------------------------------|
| v0.1.0-α  | Nix `services-flake` backend, 4 DB plugins (mysql / postgres / redis / elasticsearch)        |
| v0.1.1    | Bundled `flake.lock` per plugin (cold start 5-10 min → 30-60 s), `packages.default` for `nix run` / `nix profile install`, per-engine `ready_timeout_sec` config, honest README |
| v0.2.0    | Docker backend, hybrid `backend:` selector — explicit `nix` / `docker` in YAML, or auto-detect (Nix-with-flakes present → Nix, else Docker daemon → Docker, else clear error) when the field is omitted |
| v0.3.0    | Plugin conformance suite + CI matrix on real Docker — plugin authors verify their contract end-to-end with one test func, four bough-internal plugins are gated on `ubuntu-24.04` + `ubuntu-24.04-arm` × `mysql` / `postgres` / `redis` / `elasticsearch` |
| v0.4.0    | Generic engine plugin orchestrator (was: DB-only). `DBProvider` → `EngineProvider`, `plugins/db/` → `plugins/engine/`, YAML schema v2 (`.bough.yaml` / `engines:` / `port_ranges:` per role / `initial_resources:`). Multi-port engines (rabbitmq AMQP+Management, kafka broker+controller, NATS client+monitor+cluster) are first-class; v0.4.x reads every v0.3 surface with a deprecation warning, removed in v0.5.0 |
| v0.5.0    | Per-worktree memory orchestration layer (`instinct.enabled: true`, opt-in). 4 plugin contracts frozen: `MemoryBackend` (7 RPCs) + `InstinctMinter` (1 RPC) ship working; `CapabilityCompiler` + `SkillEvaluator` frozen as stubs for v0.6/v0.7+. SQLite reference-fallback plugin (`modernc.org/sqlite` pure Go + FTS5 + WAL). Stdin ingest as primary observer; `.jsonl` file watch opt-in beta. v0.3 pluginhost fallback removed (breaking for v0.3.x plugin binaries). See [docs/INSTINCTS.md](docs/INSTINCTS.md). |
| v0.6+     | mem0 official memory plugin, Graphiti optional plugin, `CapabilityCompiler` materialised + `bough capability compile` + Claude Skills / Agent Skills / MCP export (round 3 AI #3: tools/resources/prompts split), `bough-mcp-server` companion. Plugin signing enforcement. Reference rabbitmq / kafka / NATS / minio engine plugins. Homebrew tap. |

[embedded-postgres]: https://github.com/fergusstrange/embedded-postgres

## Status

v0.4.0 (current). The four bundled engine plugins
(`bough-plugin-{mysql,postgres,redis,elasticsearch}`) are battle-tested
in a real Go + Rails + Remix multi-sub-repo monorepo (MySQL 8.4 LTS +
Redis 7 + Elasticsearch 7) on the Docker backend; the Nix backend
remains supported via auto-detect and is the default when nix-with-
flakes is on `PATH`. The Postgres plugin is integration-test-only.
Multi-port engines (rabbitmq / kafka / NATS) are first-class in the
contract from v0.4.0 onward — reference plugins land in v0.5+.

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
make conformance-all                       # all four
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
