# bough engine plugin contract (v0.4.0+)

> This document is the canonical list of invariants a
> `bough-plugin-<kind>` binary must uphold under the v0.4.0 EngineProvider
> contract. The `bough/conformance` test suite exercises these clauses
> against a real Docker container through the RPC surface; it cannot see
> container identity or naming, and some clauses say what it does not
> check.
>
> **v0.4.0 rename**: `DBProvider` → `EngineProvider`, `plugins/db/` →
> `plugins/engine/`. See `docs/MIGRATION-v0.3-to-v0.4.md` for the wire
> shape diff (`Port int` → `[]PortSpec`, `InitialDatabases []string` →
> `[]ResourceSpec`).

Plugin authors: passing `conformance.Run(t, cfg)` is the minimum; the
clauses marked as not checked are still yours to uphold. If a clause below describes a behaviour you
cannot simulate (e.g. you have no socket layer to preempt with a sidecar
listener), set the corresponding `Skip*` flag in `conformance.Config`
and the suite will treat the clause as not-applicable rather than failed.

## Lifecycle

1. **`Up` creates one Docker container** named `bough-<engine>-<port>`,
   where `<port>` is the value of the `Role: "main"` entry in
   `UpReq.Ports` (or the first entry for engines that emit a single
   port). Multi-port engines bind every entry in `Ports`.
2. **`Up` pulls the image** declared by `Extras["docker.image"]`, else the
   one the plugin derives from `Extras["version"]`, else the plugin
   default, when it is not already cached. Pull failures surface as a
   non-nil error from `Up`; the suite asserts this via
   `Fault_ImagePullFailure`. A version the active backend cannot honour
   MUST come back as an `Up` error naming a version that works — never as
   a silent substitution. The host expands `{{ .Version }}` in each
   `PluginSpec.Location` before `Up`, so a plugin sees a finished URL.
3. **`Up` is up-or-reuse**: if a container with the canonical name is
   already running the image `Up` resolved, `Up` returns nil without
   recreating it. A different live owner worktree is refused. A different
   gone owner causes removal after the image check and recreation, even
   while running; `Down` may still stop it. Compose must not inherit that
   container's project and named volumes.
   The four native Docker plugins record the image ref and
   immutable image ID in `<datadir>.image` before container creation, and
   on reuse record the running container's image ID. `CheckDatadirImage` leaves
   the image check to `UpOrReuse` only for a named container whose writable
   absolute bind source points to this datadir; otherwise it consults the
   record for old data.
   A different image is refused with both refs named; different refs are
   accepted only when the wanted ref resolves to the recorded image ID.
   Data with neither a record nor a container binding this datadir has an
   unknown writer and is refused (`HaveImage` empty); the remedy offers
   starting over first and recording the wanted ref last, only for data the
   wanted image is known to have written.
   A missing or readable empty datadir makes a stale record irrelevant.
   The error names the datadir and record
   (`dockerutil.ImageMismatchError.WithDatadir`). To keep the worktree,
   run `docker rm -fv <container>` only for the container named by the error,
   if any; a record mismatch names no container. Then empty
   the datadir: delete everything inside it and keep the directory itself.
   All data in that directory is lost. Then record the wanted ref in the
   record (the error prints the `printf`), since engine permissions can
   prevent checking that the datadir is empty. With a container binding this
   datadir and no record, `Up` first records that container's image, so a failed
   recreate leaves the writer known. On Linux these steps may require root.
   Resume the same worktree the same way it was created (the WorktreeCreate hook, or `bough create`
   with the same worktree name). `Cleanup` removes the record with the datadir
   through `dockerutil.RemoveDatadir`. Compose owns its data and is exempt.
   The suite checks reuse of the unchanged image in the `UpReuse` phase: it
   calls `Up` again while the service is up, requires a nil return, then re-checks
   readiness. It does not compare container identity, so a plugin that
   recreates the container can still pass.
   (Running the full lifecycle `IdempotentCount` times, by contrast,
   only exercises restart: each loop Downs before the next Up.)
4. **`Up` surfaces port conflicts** as a non-nil error.
5. **`ReadyCheck` does not return true until the service accepts at
   least one protocol-level message** on every entry in `Ports`. A TCP
   listen alone is not enough — the official mysql, postgres, redis and
   elasticsearch images all open the TCP socket before the daemon
   itself is ready. The suite checks the protocol level only when you
   set `Config.NativeProbe`. The bundled `compose` plugin is the
   exception: it defaults to a TCP dial unless the operator sets
   `extras.compose.ready_probe`.
6. **`Down` is graceful within `GracefulTimeoutSec`**. After that
   deadline the plugin must SIGKILL the workload. `Down` returns only
   once the engine is stopped (the host's `Cleanup` and `bough remove`
   rely on it): the suite checks that it returns nil and that every port
   stops accepting connections on 127.0.0.1 within 10 s. `Down` and `ReadyCheck`
   carry no backend token, so a plugin that registers more than one
   backend must pick the one whose resource is actually *running* (a
   stopped container with the canonical name does not count);
   `api.Backends.ForPort` does this, and a single backend
   short-circuits it. With no backend running, `ForPort` falls back to
   the default backend, so an engine whose `Up` is still in flight on a
   second backend resolves to the wrong one: registering a second
   backend also means carrying the backend token on
   `DownRequest` / `ReadyCheckRequest`.
7. **`Cleanup` removes the datadir and is idempotent.** After a nil
   return the datadir must be gone, and a second `Cleanup` on the same
   `datadir` + `ports` must return nil. The suite skips, rather than
   fails, a permission error from a datadir the container's user owns;
   the bundled engines avoid it with `dockerutil.RemoveDatadir`, which
   deletes such files from a root container. A plugin that owns no
   datadir (compose) sets `conformance.Config.SkipDatadirRemovalCheck`.

## EnvVars

8. **Every value `EnvVars` returns is non-empty.**
9. **Every host:port pair `EnvVars` advertises is reachable from the
   host.** A value like
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
    `Config.AllowShellMetachars=true`. A
    `(` / `&` / `;` / `$` in a value aborts bash `source .env.local`.

## Datadir

11. **`Up` is not required to surface an un-writable `Datadir`.** A
    container backend only bind-mounts the directory and lets the engine
    write there, so the failure arrives asynchronously, long after `Up`
    returned nil. The suite's `Fault_DatadirPermission` asserted the
    opposite for the host-process backend bough used to bundle; it was
    retired with that backend and nothing replaces it. A plugin that
    does prepare `Datadir` synchronously may still do so — it is simply
    not checked here.

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

- A detached, long-lived subprocess launched from `Up` (e.g. a
  supervisor meant to outlive the RPC call) must not be started with
  the incoming `Up` context directly: go-plugin's gRPC transport
  cancels that context the instant `Up` returns, and Go's
  `exec.CommandContext` watchdog then kills the child within
  microseconds regardless of `Setsid`. Use `context.WithoutCancel(ctx)`
  (or an equivalent detached context) for the subprocess, and reap it
  in a goroutine afterward to avoid a zombie.
- `extras["backend"]` names the lifecycle implementation. The host
  sends `api.DefaultBackend` for an engine whose YAML omits `backend:`,
  and the conformance suite stamps the same token when `cfg.Extras`
  carries none — pass one there to exercise another backend your plugin
  registers. An unregistered token must be refused, not silently
  swapped: `api.Backends.ForUp` returns an `*api.UnknownBackendError`
  naming what the plugin does provide.
- `MainPortRole` on `conformance.Config` defaults to `"main"`; override
  it if your plugin's "primary" port is named differently.
- The contract bound is on plugins/engine/api/proto/engine.proto. Any
  field added after v0.4.0 must use a new tag number and ride along
  with a ProtocolVersion bump in handshake.go.
