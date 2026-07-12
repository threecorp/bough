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
first four defers the actual lifecycle (`up` / `ready check` / `down`)
to a backend you choose: today that's [services-flake][services-flake]
on top of Nix or a direct Docker SDK backend; the host's auto-detect
picks one based on what's on the runner. `compose` is different by
design — instead of provisioning its own engine, it wraps an EXISTING
`docker-compose.yml`/service an operator already has, giving it only
worktree-scoped port isolation (see [Compose-wrapped
services](#compose-wrapped-services) below).

The "what to isolate" is fully declarative — pick which repositories
appear under `worktrees/<name>/` and which engines spawn per worktree
via a single YAML at the monorepo root. Engines are loaded as gRPC
plugins, so adding a new engine (rabbitmq, kafka, nats, minio, …) never
requires editing the host binary.

[services-flake]: https://github.com/juspay/services-flake

## Cost & billing

**As of 2026-06-27, with Claude Code's current subscription model, bough
runs entirely inside your existing Claude Code subscription — it makes no
separate Anthropic API call and incurs no separate API billing.** How
that holds up:

- **The worktree-isolation core** (`bough create`, the engine plugins,
  `.env.local` rendering) makes **zero** LLM calls — it is pure local
  infrastructure (git, ports, Nix/Docker).
- **The continuous-learning feature** (observe → evolve → inject) reaches
  an LLM **only** by spawning `claude --print` as a subprocess, which
  reuses your operator subscription auth (`~/.claude.json` oauth token).
  bough strips `ANTHROPIC_API_KEY` / `ANTHROPIC_AUTH_TOKEN` /
  `ANTHROPIC_BASE_URL` / Bedrock / Vertex / `CLAUDE_API_KEY` from the
  subprocess env, so it cannot silently flip to API-key billing. There is
  no Anthropic SDK, and no HTTP client to any model endpoint, anywhere in
  the binary.
- **The hooks Claude Code fires automatically** — `PreToolUse`,
  `PostToolUse`, `UserPromptSubmit`, `Stop`, `SessionEnd`, `PreCompact` —
  are **pure filesystem**: they append an observation line and (for
  `UserPromptSubmit`) print a small instinct block to stdout. They make
  **no** LLM call. The `UserPromptSubmit` block is folded into your next
  turn as ordinary input tokens of your own session — the same as any
  context — not a separate charge.
- **LLM calls happen only in the explicit commands** `bough observer
  run-once` and `bough evolve --generate` (and the opt-in `bough observer
  start` daemon) — each one `claude --print` under your subscription, hard
  rate-limited (10 / session, 30 / hour, 3-failure circuit breaker).

Run `bough doctor` to confirm the posture (it warns if an
`ANTHROPIC_API_KEY`-style variable in your shell would override
subscription auth).

> ⚠️ This reflects Claude Code's billing model **as of 2026-06-27**. bough
> relies on `claude --print` subprocess invocations being covered by the
> Claude Code subscription. If Anthropic changes how Claude Code meters
> `--print` / subscription usage, this could change — that is outside
> bough's control, so verify against current Claude Code pricing if it
> matters to you.

## Prerequisites

bough binaries themselves are static Go executables (darwin / linux,
arm64 / amd64) — `bough` never installs Nix or Docker for you. The
host auto-detects which backend each plugin uses with a v0.1.x-compat
preference for `nix` (so monorepos that adopted bough when nix was
the only option do not silently flip to docker on upgrade):

  1. nix-with-flakes on `PATH` → `nix`
  2. else docker daemon reachable → `docker`
  3. else: actionable error pointing at the `engines[].backend` YAML knob

In practice that means **nix users with flakes enabled** (= those who
already installed Nix and turned on the `nix-command`/`flakes`
experimental features before adopting bough) get `nix`; **everyone
else (the typical install, including a bare Nix install without
flakes)** gets `docker`. An explicit `backend: nix | docker` per
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
#   xattr -dr com.apple.quarantine ~/.local/bin/bough ~/.local/bin/bough-plugin-* 2>/dev/null
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
# brew tap     ikeikeikeike/tap
# brew install bough
```

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
  #   version: "7"
  #   port_ranges:
  #     main: [56000, 58999]
  #   extras:
  #     es.mem_limit: "2g"          # docker --memory cap (default: 2x heap) — guards the host VM from OOM
  #     es.config_mount: "demo-api/es-config/analyzer"   # host dir, relative to the monorepo worktree root
  #   plugins:
  #     - id: analysis-icu          # official plugin: id only
  #     - id: analysis-example      # third-party plugin: id + a direct download URL
  #       location: "https://example.com/analysis-example-7.17.0.zip"

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

Then wire it into Claude Code's `WorktreeCreate` / `WorktreeRemove`
hooks in `.claude/settings.json`. `bough hook install` writes these
(and the continuous-learning hooks) for you, all routed through the
single `bough hook handle` dispatcher:

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
> (a second `post_create` migration pass). `bough hook install` only ever
> manages its own `bough hook handle` entries, so prefer it and don't also
> hand-add a `bough create --stdin-json` group.

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

> **Upgrading from a pre-v0.11 layout is transparent.** A monorepo whose
> checkouts still sit at `<root>/<name>` and whose worktrees still live
> under `.worktrees/` keeps working unchanged — bough detects and reuses
> the existing locations, and only fresh checkouts / worktrees adopt the
> new paths. To fully consolidate, move the sub-repo checkouts into
> `.bough/repos/`, rename `.worktrees/` → `worktrees/`, and (optionally)
> `.bough-ports.json` → `.bough/ports.json`.

## Compose-wrapped services

The four bundled engines above are ones bough fully provisions itself
(nix flake or a bough-managed Docker container). `kind: compose` is
different: it wraps a `docker-compose.yml` you already have — no
duplicate nix flake, no second source of truth for the image/version —
and gives it only the one thing bough is actually good at:
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
  `postgres` / `http`) on the engine entry if you want a real
  protocol check instead.
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

# Continuous learning (opt-in; instinct.enabled: true)
bough hook install | uninstall          # Claude Code hook wiring in .claude/settings.json
bough doctor                            # hook wiring + observer capture + cost posture
bough observer run-once | start         # mint instincts via claude --print
bough instinct list | show <id>         # inspect the captured corpus
bough evolve --generate                 # cluster instincts → skills / agents / commands
bough ecc import                        # interop with an everything-claude-code corpus
bough inject-context                    # UserPromptSubmit instinct block (hook)
bough preserve-instincts                # PreCompact snapshot (hook)
bough session-end                       # SessionEnd summary + confidence (hook)
```

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
│   ├── hooks/                              post_create / pre_remove hook runner
│   ├── backend/                            nix / docker backend auto-detect
│   ├── pluginhost/                         go-plugin discovery + lifecycle
│   ├── pluginsign/                         plugin binary signature verification
│   │                                       # continuous learning (v0.9)
│   ├── homunculus/                         instinct corpus (~/.local/share/bough-homunculus/)
│   ├── observe/                            observations.jsonl writer + Anthropic-env scrub
│   ├── prompts/                            //go:embed prompt templates + 3-layer override
│   ├── provider/claudecli/                 `claude --print` subprocess + rate limiter
│   ├── evolve/                             instinct → skill / agent / command 5-gate pipeline
│   ├── qualitygate/                        operator-supplied lint / typecheck gates
│   ├── inject/                             UserPromptSubmit context-block builder
│   └── session/                            SessionEnd summary + confidence update
├── plugins/
│   └── engine/
│       ├── api/                            gRPC EngineProvider contract + Go interface
│       ├── mysql/                          MySQL 8.4 provider + embedded services-flake
│       ├── postgres/                       PostgreSQL 16 provider + embedded services-flake
│       ├── redis/                          Redis 7 provider + embedded services-flake
│       ├── elasticsearch/                  Elasticsearch 7 provider + process-compose-flake
│       └── compose/                        Wraps an existing docker-compose.yml/service
├── tests/
│   └── integration/                        real-services E2E (build tag: integration)
├── flake.nix                               devShells.ci / devShells.default
├── .goreleaser.yaml                        cross-compile + GitHub Release
└── .github/workflows/                      ci.yml + release.yml
```

## Continuous learning (v0.9)

> **bough is not an agent memory system. bough is a per-worktree
> memory-orchestration layer.** The continuous-learning loop is a
> verbatim Go port of the [everything-claude-code][ecc] reference
> architecture, so every LLM call stays inside your Claude Code
> subscription (see [Cost & billing](#cost--billing)).

[ecc]: https://github.com/affaan-m/everything-claude-code

The loop is **observe → evolve → inject**, entirely opt-in and off by
default:

1. **Observe.** Claude Code hooks (`SessionEnd`, `PreCompact`,
   `UserPromptSubmit`, …) append raw session events to
   `observations.jsonl` and mint *instincts* — confidence-scored
   behavioural rules — into an on-disk corpus (the "homunculus") under
   `~/.local/share/bough-homunculus/<project-id>/` (`project-id` =
   `sha256[:12]` of the credential-stripped git remote, else the repo
   path). Env scrubbing strips every `ANTHROPIC_*` / Bedrock / Vertex
   key so a spawned `claude --print` can never flip to API billing.
2. **Evolve.** `bough evolve --generate` clusters related instincts
   through a 5-gate pipeline (the final gate is an LLM judge via
   `claude --print --output-format json`) and emits Claude Code
   artifacts — `SKILL.md`, agents, and commands — into the repo's
   `.claude/` (project-scope since v0.9.20; `bough create` symlinks
   each worktree's `.claude/skills` at the monorepo copy). Generated
   artifacts cite a resolvable source-instinct path so a reader can
   trace a skill back to the instincts it came from (v0.9.22).
3. **Inject.** The `UserPromptSubmit` hook prints a confidence-ranked
   instinct block into your next turn as ordinary input tokens — no
   separate call.

```sh
# Wire bough's hook handlers into .claude/settings.json (idempotent;
# hand-edited rows are preserved) and inspect the posture.
bough hook install
bough doctor                     # hook wiring + observer capture + cost meter

# Mint instincts from recent observations, then review the corpus.
bough observer run-once          # one claude --print pass
bough instinct list              # confidence-ranked corpus
bough instinct show <id>

# Cluster the corpus into skills / agents / commands.
bough evolve --generate          # 5-gate pipeline; writes <repo>/.claude/*

# Interop with an existing everything-claude-code corpus.
bough ecc import
```

Enable it per-monorepo in `.bough.yaml` (off by default):

```yaml
instinct:
  enabled: true
```

LLM calls happen **only** in the explicit `bough observer run-once` /
`bough evolve --generate` (and the opt-in `bough observer start`
daemon) — each a `claude --print` subprocess under your subscription,
hard rate-limited (10 / session, 30 / hour, 3-failure circuit
breaker). Everything else — hooks, ingest, clustering gates 1-4 — is
pure local filesystem.

See [docs/EVOLVE.md](docs/EVOLVE.md) for the 5-gate evolve pipeline.

> **v0.5-v0.8 superseded.** Earlier releases explored a different
> continuous-learning design. v0.9.0 reset to the ECC verbatim port
> above; pin **v0.8.1** if you depend on the earlier surface. See the
> v0.9.0 [CHANGELOG](CHANGELOG.md) entry.

## Roadmap

| Milestone | Headline                                                                                    |
|-----------|---------------------------------------------------------------------------------------------|
| v0.1.0-α  | Nix `services-flake` backend, 4 DB plugins (mysql / postgres / redis / elasticsearch)        |
| v0.1.1    | Bundled `flake.lock` per plugin (cold start 5-10 min → 30-60 s), `packages.default` for `nix run` / `nix profile install`, per-engine `ready_timeout_sec` config, honest README |
| v0.2.0    | Docker backend, hybrid `backend:` selector — explicit `nix` / `docker` in YAML, or auto-detect (Nix-with-flakes present → Nix, else Docker daemon → Docker, else clear error) when the field is omitted |
| v0.3.0    | Plugin conformance suite + CI matrix on real Docker — plugin authors verify their contract end-to-end with one test func, four bough-internal plugins are gated on `ubuntu-24.04` + `ubuntu-24.04-arm` × `mysql` / `postgres` / `redis` / `elasticsearch` |
| v0.4.0    | Generic engine plugin orchestrator (was: DB-only). `DBProvider` → `EngineProvider`, `plugins/db/` → `plugins/engine/`, YAML schema v2 (`.bough.yaml` / `engines:` / `port_ranges:` per role / `initial_resources:`). Multi-port engines (rabbitmq AMQP+Management, kafka broker+controller, NATS client+monitor+cluster) are first-class; v0.4.x reads every v0.3 surface with a deprecation warning, removed in v0.5.0 |
| v0.5.0-v0.8.0 | (superseded) An earlier continuous-learning design, replaced wholesale in v0.9.0; pin v0.8.1 if you depend on it |
| v0.9.0    | The "ECC verbatim port" reset. Deleted the v0.5-v0.8 surface and rebuilt continuous learning as a faithful Go port of [everything-claude-code](https://github.com/affaan-m/everything-claude-code): the `~/.local/share/bough-homunculus/` corpus, `observations.jsonl`, and a subscription-only `claude --print` mechanism (no Anthropic API, no separate billing) |
| v0.9.1-v0.9.22 | The observe → evolve → inject loop: `bough evolve --generate` 5-gate clustering into skills / agents / commands, `UserPromptSubmit` instinct injection, `SessionEnd` / `PreCompact` hooks, secret-scrub at capture, project-scope evolved skills (v0.9.20), resolvable source-instinct paths (v0.9.22), plus a retrospective `/review` bug-fix sweep of the merged infra PRs |
| next      | Reference rabbitmq / kafka / NATS / minio engine plugins, Homebrew tap |

[embedded-postgres]: https://github.com/fergusstrange/embedded-postgres

## Status

v0.9.22 (current; v0.9.23 in progress). Three of the four bundled
engine plugins (`bough-plugin-{mysql,redis,elasticsearch}`) are
battle-tested in a real Go + Rails + Remix multi-sub-repo monorepo
(MySQL 8.4 LTS + Redis 7 + Elasticsearch 7) on the Docker backend; the
Nix backend remains supported via auto-detect and is the default when
nix-with-flakes is on `PATH`. The Postgres plugin
(`bough-plugin-postgres`) is integration-test-only — it has not run in
that production monorepo. Multi-port engines (rabbitmq / kafka / NATS) are
first-class in the contract — reference plugins are not yet bundled.

The worktree-isolation core has been stable since v0.4.0. v0.5 onward
layers on the opt-in [continuous-learning loop](#continuous-learning-v09).
v0.9.0 reset that loop to a verbatim Go port of the
everything-claude-code reference architecture (subscription-only, no
API billing) and **superseded the earlier v0.5-v0.8 surface
wholesale** — pin v0.8.1 if you depend on it.

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
