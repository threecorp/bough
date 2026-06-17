# Changelog

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
