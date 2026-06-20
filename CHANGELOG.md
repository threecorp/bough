# Changelog

## v0.5.0

The "instinct primitive" release. v0.5 introduces a per-worktree
memory orchestration layer (instinct subsystem) on top of the v0.4
engine plugin model. The subsystem is opt-in — set
`instinct.enabled: true` in `.bough.yaml` to use it. Existing v0.4
monorepos see no behavioural change on upgrade.

**bough is not an agent memory system. bough is a per-worktree
memory orchestration layer.** Memory intelligence is delegated to
external OSS backends (mem0 / Graphiti / Letta, v0.6+); bough
provides the canonical schemas, scope model, safety pipeline
(redaction, poisoning guard, dedupe, decay), and conformance
contract.

### Added

- **Four new plugin contracts**: `plugins/{memory,instinct,
  capability,evaluator}/api/`. v0.5 ships memory (with 7 RPCs:
  Health, Capabilities, Store, Query, Forget, Export, Import)
  and instinct (Mint) as working contracts; capability and
  evaluator are frozen as stubs for v0.6 / v0.7+.
- **Canonical schemas**: `pkg/schema/` declares TraceBundle,
  InstinctCandidate, Instinct, CapabilityArtifact (12 minimal
  fields + Payload json.RawMessage escape hatch), Scope,
  EvidencePolicy, RetrieveBudget.
- **SQLite reference-fallback plugin** (`plugins/memory/sqlite/`):
  modernc.org/sqlite (pure Go, no CGO) + FTS5 + WAL +
  busy_timeout + metadata escape hatch column. Passes the full
  conformance/memory suite (Lifecycle + Bloat + Concurrency).
- **Host coordinator** (`internal/instinct/`): redaction,
  source-aware confidence policy, poisoning guard with hybrid
  mint mode, decay scheduler, scope promote, events.jsonl audit.
- **Stdin ingest** as the PRIMARY observer path:
  `make test 2>&1 | bough instinct ingest --stdin --source test_failure`.
- **Claude `.jsonl` file watch** as opt-in beta with inode-
  rotation + truncate handling (`internal/observer/`).
- **CLI subcommands**: `bough instinct {status, mint, ingest,
  approve, query, forget, promote, export, import}` and
  `bough memory {status, query, forget, export}`. `bough memory
  status` emits a stderr NOTICE when the backend is the SQLite
  reference-fallback so users see the "consider mem0 / graphiti"
  signal every time they probe.
- **Conformance suites**: `conformance/memory/` (Lifecycle,
  Bloat, Concurrency) and `conformance/instinct/` (Lifecycle),
  with in-tree mock plugins and a TestSelf entrypoint.
- **Plugin templates**: `examples/memory-plugin-template/` and
  `examples/memory-plugin-mem0-skeleton/`.
- **Documentation**: `docs/INSTINCTS.md`, `docs/BACKENDS.md`,
  `docs/EXTERNAL_MEMORY_BACKENDS.md`,
  `docs/MEMORY_PLUGIN_AUTHOR_GUIDE.md`,
  `docs/NAMESPACE_MAPPING.md`, `docs/SECURITY.md`,
  `docs/ROADMAP.md`.

### Removed (breaking for v0.3 plugin binaries)

- **`internal/pluginhost`** drops the v0.3 DBProvider fallback
  handshake. v0.3.x plugin binaries no longer spawn under a v0.5
  host. Users running an old plugin binary must rebuild against
  `plugins/engine/api/` from v0.4.0 or later. The legacy
  `pickDatabaseNames` helper and `legacyEngineAdapter` are also
  removed.

### Changed

- `.bough.yaml` schema gains `instinct:`, `memory_backends:`, and
  `export:` sections. All are opt-in (empty/absent → subsystem
  disabled). `schema_version` stays at 2.
- GoReleaser now produces 6 binaries: the existing host + four
  engine plugins, plus the new `bough-plugin-memory-sqlite`.
- CI matrix splits engine-conformance and memory-conformance into
  separate jobs so the SQLite plugin's WAL / concurrency tests do
  not contend with the engine plugin's docker pre-pull.

## v0.4.1

Docs / user-visible-string cleanup follow-up to v0.4.0. No
behaviour change. Most strings that still read like v0.3 were
updated; v0.3 references in CHANGELOG history, MIGRATION docs,
fallback impl, and the legacy migrateLegacy() test fixture stay
intentional.

### Changed (docs / strings only)

- **cobra help text** (`bough --help`, `bough config --help`,
  `bough plugins --help`, `bough backfill --help`, `bough list --help`):
  now points at `.bough.yaml` / `.bough-ports.json` / `~/.bough/backups/`
  with a one-clause "v0.3 ... accepted on fallback" note. `bough plugins`
  reads "List engine plugins discoverable on PATH" (was "List DB
  plugins").
- **Rendered `.env.local` footer** is now `# Do not commit. Manage via
  '.bough.yaml' at the monorepo root.` (every freshly-created worktree
  picks up the new wording on the next `bough create`).
- **`backend.Detect` error message** now points at `engines[].backend`
  in `.bough.yaml` instead of `databases[].backend` in
  `.worktree-isolation.yaml`.
- **Doc comments** in `internal/backend/`, `internal/envwriter/`,
  `internal/gitwt/` updated to v0.4 canonical names.
- **`tests/integration/e2e_mysql_test.go`** fixture bumped to v0.4
  canonical (`schema_version: 2` / `engines:` / `port_ranges:` /
  `initial_resources:` / `role: engine-provider` / `.bough.yaml` /
  `.bough-ports.json` / registry key `mysql.main`). The v0.3 →
  v0.4 migrateLegacy() path stays covered by the existing
  `config_test.go` unit tests.

### Docs

- **README.md** Quick start uses `.bough.yaml` + schema_version 2 +
  `engines:` + `port_ranges:` + `initial_resources:`. Status section
  reflects v0.4.0 reality. Prerequisites now spells out the auto-detect
  order (nix → docker, deliberate v0.1.x compat preference) and the
  table puts docker first (= typical install).
- **`docs/PLUGIN_AUTHOR_GUIDE.md`** gains a Multi-port engines section
  (rabbitmq AMQP+Management, kafka broker+controller, NATS
  client+monitor+cluster) with the rabbitmq author's view of
  `PortRangeDefault` / `Up` / `EnvVars` / `Config.MainPortRole`.
- **`docs/MIGRATION-v0.3-to-v0.4.md`** past-tenses "v0.4.0 will keep
  working" → "keeps working" now that v0.4.0 has shipped.
- **`examples/plugin-template/`** README + conformance_test.go gain a
  Multi-port section pointing at the PLUGIN_AUTHOR_GUIDE walkthrough
  and a `MainPortRole` TODO marker.
- **`plugins/db/api/CONTRACT.md`** deleted — superseded by
  `plugins/engine/api/CONTRACT.md`. The legacy Go fallback files
  stay for v0.3.x plugin binary compat.

### Refactor (developer-only)

- **`internal/smoketool/`** extracts the shared Up → ReadyCheck → Down →
  Cleanup lifecycle so the four `cmd/_smoke-docker-<kind>/` binaries
  shrink to ~15-line `main()` calls that only spell out their plugin
  and per-engine tunables.
- **`conformance/lifecycle.go::runLifecycle`** 172 → 50 lines via per-
  phase helpers (`runUpPhase` / `runReadyCheckPhase` / `runEnvVarsPhase`
  / `runDownPhase` / `runOneIteration` / `assertCleanup`).
- **`internal/cli/create.go::runCreate`** 211 → 70 lines via
  `allocateAllPorts` / `startEngines` / `materializeRepositories` /
  `renderEnvLocals` / `runPostCreateHooks` / `detectBackendIfNeeded`.
  The awkward `interface{ Write([]byte) (int, error) }` parameter type
  is replaced with `io.Writer`.

### CI

- `.github/workflows/ci.yml` conformance matrix points at
  `./plugins/engine/<plugin>/...` (was missed when `plugins/db/` was
  renamed to `plugins/engine/` in v0.4.0; the previous post-v0.4.0
  matrix runs against ad-hoc PRs would not have caught this).

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
