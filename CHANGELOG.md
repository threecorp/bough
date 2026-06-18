# Changelog

## v0.4.0

bough generalises from "DB plugin orchestrator" to "engine plugin
orchestrator". Middleware (rabbitmq, kafka, nats, minio, …) can now be
written as plugins on the same lifecycle as the existing DB plugins.
The breaking changes are intentionally collected into one release so
plugin authors pay the cost once. The v0.4.x line keeps fallbacks for
every renamed surface so existing deployments do not have to migrate
in lockstep — they will be removed in v0.5.0. See
[`docs/MIGRATION-v0.3-to-v0.4.md`](./docs/MIGRATION-v0.3-to-v0.4.md)
for the full diff.

### Changed (breaking, with fallback)

- **`DBProvider` → `EngineProvider`.** The gRPC contract is renamed,
  `UpReq.Port int` becomes `UpReq.Ports []PortSpec`, `InitialDatabases
  []string` becomes `InitialResources []ResourceSpec`,
  `PortRangeDefault` returns `map[string]PortRange` (one entry per
  role). Single-port plugins keep the v0.3 shape via `Role: "main"`
  (or empty, treated identically). See
  [`plugins/engine/api/CONTRACT.md`](./plugins/engine/api/CONTRACT.md).
- **`plugins/db/` → `plugins/engine/`.** The four bundled plugins
  (mysql / postgres / redis / elasticsearch) move to the new path.
  External plugins need to update their Go module import from
  `github.com/ikeikeikeike/bough/plugins/db/api` to
  `github.com/ikeikeikeike/bough/plugins/engine/api`.
- **YAML schema_version 2.** `databases:` → `engines:`,
  `port_range: [a, b]` → `port_ranges: { main: [a, b] }`,
  `initial_databases: ["foo"]` → `initial_resources: [{ type:
  database, name: foo }]`. The host loader still accepts the v0.3
  field names with a deprecation warning and converts them in memory.
- **File / dir / handshake renames.**
  `.worktree-isolation.yaml` → `.bough.yaml`,
  `.worktree-ports.json` → `.bough-ports.json`,
  `~/.claude/backups/` → `~/.bough/backups/`,
  gRPC handshake magic cookie `BOUGH_DB_PLUGIN` → `BOUGH_ENGINE_PLUGIN`.
  Every old surface is read/honoured during the v0.4.x line; the host
  attempts the new handshake first and falls back to the v0.3 one so
  v0.3.x plugin binaries still spawn under a v0.4.x host.
- **Repository role rename.** `role: db-provider` →
  `role: engine-provider` (the YAML accepts both during v0.4.x).
- **Registry composite key.** Engine entries are now stored as
  `<kind>.<role>` (e.g. `mysql.main`); legacy keys without a dot are
  upgraded on load so existing `.worktree-ports.json` files keep
  their port allocations.

### Added

- **Multi-port engines.** Plugins declare one role per listen point
  from `PortRangeDefault`; the host allocates a deterministic port
  per role; `EnvVars` emits `BOUGH_<ENGINE>_HOST` (shared) plus
  `BOUGH_<ENGINE>_<ROLE>_PORT` / `_URL` per role. The conformance
  suite exercises the full multi-port lifecycle end-to-end against
  the in-tree mock plugin (`TestRun_MockPlugin_MultiPort_GreenPath`).
- **`conformance.Config.MainPortRole`** (default `"main"`). Targets
  the fault tests at a single role on multi-port plugins; the
  lifecycle still iterates over every declared role.
- **`AssertReachable` longest-prefix host lookup.** A `*_<ROLE>_PORT`
  key now pairs back to the nearest ancestor `*_HOST` instead of
  requiring a per-role `*_HOST` duplicate.
- **Shim helpers** `api.PickMainPort([]PortSpec)` and
  `api.PickFirstResourceName([]ResourceSpec, type)` in
  `plugins/engine/api/shims.go` keep single-port engine internals
  signature-compatible with the v0.3.x docker/nix code.
- **`docs/MIGRATION-v0.3-to-v0.4.md`** — side-by-side YAML +
  plugin-author checklist + fallback table + v0.5.0 removal timeline.
- **`docs/PLUGIN_AUTHOR_GUIDE.md` multi-port section** — rabbitmq
  author's view of `PortRangeDefault` / `Up` / `EnvVars` /
  `MainPortRole`.
- **`examples/plugin-template`** — Multi-port section in README,
  `MainPortRole` TODO marker in the conformance template, canonical
  paths throughout.

### Notes for plugin maintainers

Existing v0.3.x plugin binaries still spawn under v0.4.x via the
fallback handshake. To target v0.4 natively:

1. Update the import path
   (`plugins/db/api` → `plugins/engine/api`).
2. Switch the lifecycle signatures (`req *UpReq` taking `Ports
   []PortSpec`; `ReadyCheck(ctx, ports []int, ...)`; `Cleanup(ctx,
   datadir string, ports []int)`; `PortRangeDefault` returning
   `map[string]PortRange`).
3. Rebuild and re-run the conformance suite against bough/conformance
   v0.4.0.

The v0.5.0 release removes the v0.3 fallbacks — plugins that have
not been rebuilt by then will stop loading.

## v0.3.0

### Added

- **Plugin conformance suite** (`bough/conformance`) — plugin authors verify
  their go-plugin server against the bough contract with one test function:

  ```go
  //go:build conformance
  func TestMyPluginConformance(t *testing.T) {
      conformance.Run(t, conformance.Config{
          PluginBinary: os.Getenv("BOUGH_CONFORMANCE_PLUGIN_BIN"),
          Image:        "myengine:1.0",
      })
  }
  ```

  The suite spawns the binary under hashicorp/go-plugin (the same path
  bough's host uses in production), drives `PortRangeDefault → Up →
  ReadyCheck → EnvVars → Down → Cleanup` with idempotency, asserts the
  v0.2.5 (shell metachar) and v0.2.6 (container bridge-IP advertise)
  invariants mechanically, and runs three fault injections (port
  conflict, datadir permission, image pull failure).

- **CI conformance matrix** (`.github/workflows/ci.yml`) — every PR runs
  the suite on `ubuntu-24.04` + `ubuntu-24.04-arm` × `mysql` /
  `postgres` / `redis` / `elasticsearch`, eight cells in parallel.

- **`Makefile`** gains `conformance-local PLUGIN=<kind>` and
  `conformance-all` targets so plugin authors can verify against
  Docker Desktop / OrbStack / Colima on macOS.

- **Plugin author template** (`examples/plugin-template/`) — copy this
  directory and fill in four TODO markers to start a new plugin.

- **Plugin contract documentation** (`plugins/db/api/CONTRACT.md`) and
  **author guide** (`docs/PLUGIN_AUTHOR_GUIDE.md`).

### Fixed

- The bough host's `internal/pluginhost` exposes `DiscoverFromBinary` so
  the conformance suite can pin an exact binary path instead of
  relying on PATH lookup. Existing `Discover(kind)` wraps it.

### Plugin author notes

`conformance.Config.AllowShellMetachars=true` is the opt-out for plugins
whose URL/DSN values legitimately contain `(`, `&`, `?` (the go-sql-
driver mysql DSN format). `Skip{PortConflict,DatadirPermission,
ImagePullFailure}` are the per-fault opt-outs for backends that cannot
simulate the corresponding error path.

The four bough-internal plugins all set `SkipDatadirPermission=true`
because they only bind-mount `Datadir`; the engine inside the
container writes there, so a host-side chmod 0o000 crashes the engine
after `Up` has already returned. The downstream symptom is covered by
`AssertReachable` + `NativeProbe`.

### Follow-ups (not in v0.3.0)

- `bough conformance` CLI wrapper around `testing.MainStart` — plugin
  authors get the same coverage via `go test -tags=conformance` today.
- Plugin-side `Cleanup` chown helper so `os.RemoveAll` succeeds on
  Linux runners even after a container wrote files as a non-host uid.
  The conformance suite currently works around this with its own
  `docker run --rm alpine chown` fallback (see `conformance/datadir.go`).
- Conformance suite for the nix-services-flake backend; v0.3.0 forces
  `extras["backend"]="docker"` so the docker side is what CI verifies.

## v0.2.6

- **fix(plugins/db/elasticsearch)**: advertise host-reachable publish
  address — sniffing clients (olivere/elastic et al.) used to dial the
  container's bridge IP and crash boot.

## v0.2.5

- **fix(create)**: inject `.env.local` `KEY=VALUE` pairs directly into
  `post_create` child env. The previous `source .env.local` shelled out
  to bash, which aborted on the first `(` in any value (mysql DSN) and
  silently emptied every later `${VAR}`.

## v0.2.4

- **feat(gitwt,cli)**: fetch origin and branch off `origin/<base>` on
  `bough create` so a stale local develop does not get inherited.

## v0.2.0

- Docker backend implementation + hybrid backend selector
  (`auto-detect` on `nix` first, then `docker`).

## v0.1.1

- Bundled `flake.lock` per plugin (cold start 5-10 min → 30-60 s);
  `packages.default` for `nix run` / `nix profile install`; per-engine
  `ready_timeout_sec` config.

## v0.1.0

- First public release. Nix `services-flake` backend; 4 DB plugins
  (mysql / postgres / redis / elasticsearch); cobra CLI;
  `.worktree-isolation.yaml`-driven host.
