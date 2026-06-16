# bough

> Per-worktree isolation orchestrator for monorepos.

`bough` brings up an isolated dev environment per git worktree (per
feature branch): a deterministically-allocated port triplet, an
auto-generated `.env.local` in every sub-repo, and a worktree-local
instance of every declared database engine — all driven by one
`.worktree-isolation.yaml` at the monorepo root.

bough itself is a small Go CLI plus four database plugins
(`bough-plugin-{mysql,postgres,redis,elasticsearch}`), wired together
via Hashicorp go-plugin (gRPC over Unix socket). Each plugin defers
the actual database lifecycle (`up` / `ready check` / `down`) to a
backend you choose: today that's [services-flake][services-flake] on
top of Nix; a Docker backend is planned for v0.2 (see
[Roadmap](#roadmap)).

The "what to isolate" is fully declarative — pick which repositories
appear under `.worktrees/<name>/` and which database engines spawn per
worktree via a single YAML at the monorepo root. Database engines are
loaded as gRPC plugins, so adding a new engine never requires editing
the host binary.

[services-flake]: https://github.com/juspay/services-flake

## Prerequisites

bough binaries themselves are static Go executables (darwin / linux,
arm64 / amd64) — `bough` never installs Nix or Docker for you. What
the user needs depends on which backend each plugin uses:

| Backend                  | User must provide                                                |
|--------------------------|------------------------------------------------------------------|
| `nix` (current default)  | Nix with flakes enabled + network access to flakehub.com / github.com on first invocation |
| `docker` (planned v0.2)  | A Docker-compatible daemon (Docker Desktop / OrbStack / Colima / podman with the docker socket) |

Cold-start cost (first `bough create` invocation on a fresh machine):

| Backend                                              | Cold start          | Warm start  |
|------------------------------------------------------|---------------------|-------------|
| `nix` (v0.1.0, no bundled `flake.lock`)              | 5-10 min ⚠         | 10-60 s     |
| `nix` (v0.1.1+, bundled `flake.lock`)                | 30-60 s             | 5-10 s      |
| `docker` (v0.2+, after image pull)                   | image pull dominant | 1-5 s       |

The v0.1.0-alpha 5-10 min cold start is the reason v0.1.1 ships a
bundled `flake.lock` per plugin (no more flakehub.com round-trip on
every fresh worktree), and v0.2 adds the Docker backend so users who
prefer Docker over Nix can avoid Nix entirely.

## Install

bough ships as 5 binaries (`bough` + 4 `bough-plugin-*`). Pick one:

```bash
# 1. GitHub Release tarball (recommended; no Go / Nix toolchain needed)
#    Available for darwin/linux × arm64/amd64.
#    Replace the URL with your platform tarball from the latest release.
curl -fsSL https://github.com/ikeikeikeike/bough/releases/latest/download/bough_$(uname -s | tr A-Z a-z)_$(uname -m).tar.gz \
  | tar xz -C ~/.local/bin/  bough bough-plugin-mysql bough-plugin-postgres bough-plugin-redis bough-plugin-elasticsearch

# 2. go install (per-binary; requires Go toolchain on PATH)
go install github.com/ikeikeikeike/bough/cmd/bough@latest
go install github.com/ikeikeikeike/bough/cmd/bough-plugin-mysql@latest
go install github.com/ikeikeikeike/bough/cmd/bough-plugin-postgres@latest
go install github.com/ikeikeikeike/bough/cmd/bough-plugin-redis@latest
go install github.com/ikeikeikeike/bough/cmd/bough-plugin-elasticsearch@latest

# 3. Nix flake (v0.1.1+; requires Nix with flakes enabled)
nix run    github:ikeikeikeike/bough -- create --stdin-json
nix profile install github:ikeikeikeike/bough

# 4. Homebrew (planned — tap not yet published)
# brew tap     ikeikeikeike/tap
# brew install bough
```

The `nix run` / `nix profile install` paths land working in v0.1.1 once
`flake.nix` exports `packages.default`; **v0.1.0-alpha shipped without
`packages.default`**, so those two paths are no-ops on the alpha tag —
use the tarball or `go install` if you are on v0.1.0-alpha.

## Quick start

Drop a `.worktree-isolation.yaml` at the monorepo root that declares
which sub-repos hang off `.worktrees/<name>/` and which database
engines start per worktree:

```yaml
schema_version: 1

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
    role: db-provider
    env_local:
      DEMO_DBM_PORT: "{{ .Mysql.Port }}"
    post_create:
      # Use whichever shell / toolchain your repo standardises on —
      # bough only runs the command, it does not assume Nix here.
      - "make migrate"

databases:
  - kind: mysql           # plugin discovery key (matches bough-plugin-mysql)
    version: "8.4"
    port_range: [42000, 44999]
    socket_dir: "/tmp"
    initial_databases: ["demo"]
    # backend: nix        # optional; auto-detects nix-with-flakes / docker when omitted
    # ready_timeout_sec: 600  # v0.1.1+; default 600s for nix cold paths

ports:
  api:    { range: [45000, 47999] }

registry:
  path: ".worktree-ports.json"
  backup_dir: "~/.claude/backups"

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

1. Allocates a port triplet (db / api / gateway / …) for the branch
2. Materialises every declared sub-repo via `git worktree add`
3. Spawns the configured database engine via the matching
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
│   ├── registry/                           .worktree-ports.json atomic R/W
│   ├── gitwt/                              `git worktree` wrapper
│   ├── envwriter/                          text/template + Sprig .env.local generator
│   ├── hooks/                              post_create / pre_remove hook runner
│   ├── mcp/                                ~/.claude.json projects upsert
│   └── pluginhost/                         go-plugin discovery + lifecycle
├── plugins/
│   └── db/
│       ├── api/                            gRPC contract + Go interface
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

## Roadmap

| Milestone | Headline                                                                                    |
|-----------|---------------------------------------------------------------------------------------------|
| v0.1.0-α  | Nix `services-flake` backend, 4 DB plugins (mysql / postgres / redis / elasticsearch)        |
| v0.1.1    | Bundled `flake.lock` per plugin (cold start 5-10 min → 30-60 s), `packages.default` for `nix run` / `nix profile install`, per-engine `ready_timeout_sec` config, honest README |
| v0.2.0    | Docker backend, hybrid `backend:` selector — explicit `nix` / `docker` in YAML, or auto-detect (Nix-with-flakes present → Nix, else Docker daemon → Docker, else clear error) when the field is omitted |
| v0.3+     | `backend_options` for per-engine image / pull policy overrides, embedded backends (e.g. [`fergusstrange/embedded-postgres`][embedded-postgres]) for niche cases, multi-AI hook adapters (Cursor / Windsurf / Aider), Homebrew tap |

[embedded-postgres]: https://github.com/fergusstrange/embedded-postgres

## Status

Alpha (v0.1.x). All four DB plugins implemented; the Nix backend is
battle-tested in a real Go + Rails + Remix multi-sub-repo monorepo
(MySQL 8.4 LTS + Redis 7 + Elasticsearch 7), the Postgres plugin is
integration-test-only as of v0.1.x. Docker backend lands in v0.2.

The Coastfile-shaped competitor [`coast-guard/coasts`][coasts] solves an
adjacent problem (Coastfile = 1 git repo + application services); bough
differs in scope (`.worktree-isolation.yaml` = N independent git repos +
engine-level DB declarations + deterministic-per-branch port allocation).
Both are MIT, both target laptops, both can coexist.

[coasts]: https://github.com/coast-guard/coasts

## Contributing

Bug reports and pull requests welcome — please run `make test`,
`make lint`, and `make build` locally before opening a PR.

## License

MIT. See `LICENSE` for the full text.
