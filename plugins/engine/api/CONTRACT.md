# bough engine plugin contract (v0.4.0+)

> This document is the canonical list of invariants a
> `bough-plugin-<kind>` binary must uphold under the v0.4.0 EngineProvider
> contract. The `bough/conformance` test suite checks every clause
> mechanically against a real Docker container, so the document and the
> guard tests stay in lock-step.
>
> **v0.4.0 rename**: `DBProvider` → `EngineProvider`, `plugins/db/` →
> `plugins/engine/`. See `docs/MIGRATION-v0.3-to-v0.4.md` for the wire
> shape diff (`Port int` → `[]PortSpec`, `InitialDatabases []string` →
> `[]ResourceSpec`).

Plugin authors: if your plugin passes `conformance.Run(t, cfg)`, it
satisfies this contract. If a clause below describes a behaviour you
cannot simulate (e.g. you have no socket layer to preempt with a sidecar
listener), set the corresponding `Skip*` flag in `conformance.Config`
and the suite will treat the clause as not-applicable rather than failed.

## Lifecycle

1. **`Up` creates one Docker container** named `bough-<engine>-<port>`,
   where `<port>` is the value of the `Role: "main"` entry in
   `UpReq.Ports` (or the first entry for engines that emit a single
   port). Multi-port engines bind every entry in `Ports`.
2. **`Up` pulls the image** declared by `Extras["docker.image"]` (or the
   plugin default if absent) when it is not already cached. Pull failures
   surface as a non-nil error from `Up`; the suite asserts this via
   `Fault_ImagePullFailure`.
3. **`Up` is up-or-reuse**: if a container with the canonical name is
   already running, `Up` returns nil without recreating it. The suite
   asserts this in the `UpReuse` phase — it calls `Up` a second time
   while the service is up, requires a nil return, then re-checks
   readiness to confirm the reuse did not disrupt the running engine.
   (Running the full lifecycle `IdempotentCount` times, by contrast,
   only exercises restart: each loop Downs before the next Up.)
4. **`Up` surfaces port conflicts** as a non-nil error.
5. **`ReadyCheck` does not return true until the service accepts at
   least one protocol-level message** on every entry in `Ports`. A TCP
   listen alone is not enough — the official mysql, postgres, redis and
   elasticsearch images all open the TCP socket before the daemon
   itself is ready.
6. **`Down` is graceful within `GracefulTimeoutSec`**. After that
   deadline the plugin must SIGKILL the workload. When a plugin
   supports more than one backend (e.g. nix + docker) and self-detects
   which one is active, that detection must check the candidate
   resource is actually *running* — not merely that it exists. A
   stopped/leftover Docker container matching the engine's naming
   convention must not count as "this backend is in use": `Down`
   would then act on the wrong resource while the real, active engine
   (possibly on the other backend) keeps running untouched, and a
   later `Cleanup` would delete its `Datadir` out from under it.
7. **`Cleanup` is idempotent.** A second `Cleanup` on the same
   `datadir` + `ports` must return nil.

## EnvVars

8. **Every value `EnvVars` returns is non-empty.**
9. **Every host:port pair `EnvVars` advertises is reachable from the
   host.** This is the v0.2.6 invariant: a value like
   `BOUGH_<ENGINE>_HOST=172.17.0.4` (a container bridge IP) passes plain
   unit tests but crashes sniffing clients at boot.

   Key naming conventions the suite recognises:

   - **Single-port engines** (mysql/postgres/redis/elasticsearch and
     anything that emits one `Role: "main"` port): role-omitted form
     `BOUGH_<ENGINE>_HOST` + `BOUGH_<ENGINE>_PORT` (+ optional
     `BOUGH_<ENGINE>_URL`).
   - **Multi-port engines** (rabbitmq AMQP + Management, kafka broker +
     controller, NATS client + monitor + cluster, etc.): a single
     `BOUGH_<ENGINE>_HOST` + per-role `BOUGH_<ENGINE>_<ROLE>_PORT`
     (and optional `BOUGH_<ENGINE>_<ROLE>_URL`). The suite's
     `AssertReachable` walks every `*_PORT` key.

10. **Values are shell-safe** unless the plugin declares
    `Config.AllowShellMetachars=true`. This is the v0.2.5 invariant: a
    `(` / `&` / `;` / `$` in a value aborts bash `source .env.local`.

## Datadir

11. **On the host-process backend, `Up` prepares `Datadir` and surfaces
    an un-writable parent as a non-nil error.** The services-flake /
    process-compose path mkdirs `Datadir` (or its parent, for engines
    like postgres that must not pre-create `$PGDATA`) synchronously
    inside `Up`, before it launches the engine — so a `chmod 0o000`
    parent is a real `Up` failure. The suite's `Fault_DatadirPermission`
    asserts this, forcing the host-process backend via
    `DatadirFaultBackend` (default `"nix"`). The docker backend
    legitimately only bind-mounts `Datadir` and lets the container write
    there, so it does not surface this error; a plugin that is
    docker-only (no host-process path) opts out with
    `SkipDatadirPermission=true`.

## Plugins (optional)

12. **`UpReq.Plugins` (proto `repeated PluginSpec plugins = 7`, v3) is a
    best-effort, engine-optional hint.** Each entry is an `{ID, Location}`
    pair mirroring Elasticsearch's own `elasticsearch-plugins.yml` fields
    (Location empty = official plugin known by ID; non-empty = a direct
    download URL for a third-party plugin). A plugin that does not manage
    plugins (mysql, redis, postgres, compose) MUST ignore the list — an
    empty OR non-empty `Plugins` must not change its behaviour. The
    conformance suite passes an empty list, so this clause is not
    separately asserted; it exists so a host can populate the field
    unconditionally without breaking plugins that do not consume it.

## Notes for plugin authors

- A detached, long-lived subprocess launched from `Up` (e.g. a nix
  daemon meant to outlive the RPC call) must not be started with the
  incoming `Up` context directly: go-plugin's gRPC transport cancels
  that context the instant `Up` returns, and Go's
  `exec.CommandContext` watchdog then kills the child within
  microseconds regardless of `Setsid`. Use `context.WithoutCancel(ctx)`
  (or an equivalent detached context) for the subprocess, and reap it
  in a goroutine afterward to avoid a zombie.
- `extras["backend"]="docker"` is forced by the conformance suite by
  default so the docker path is always exercised. Pass
  `cfg.Extras["backend"]="nix"` to verify the services-flake path.
  `Fault_DatadirPermission` is the one exception: it forces the
  host-process backend (`DatadirFaultBackend`, default `"nix"`) because
  only that path prepares `Datadir` synchronously and can surface an
  un-writable parent as an `Up` error.
- `MainPortRole` on `conformance.Config` defaults to `"main"`; override
  it if your plugin's "primary" port is named differently.
- The contract bound is on plugins/engine/api/proto/engine.proto. Any
  field added after v0.4.0 must use a new tag number and ride along
  with a ProtocolVersion bump in handshake.go.
