# Writing a bough plugin

A bough plugin is a single Go binary named `bough-plugin-<kind>` that
serves Hashicorp's `go-plugin` gRPC protocol. The host (`bough create`)
discovers it by name on `PATH`, calls `Up` / `ReadyCheck` / `EnvVars` /
`Down` / `Cleanup`, and uses the values you return to render the
worktree's `.env.local`.

This guide shows how to verify your plugin against the bough contract
end-to-end with a single test function.

## TL;DR

```go
// myplugin/conformance_test.go
//go:build conformance

package myplugin_test

import (
    "os"
    "testing"
    "time"

    "github.com/ikeikeikeike/bough/conformance"
)

func TestMyPluginConformance(t *testing.T) {
    bin := os.Getenv("BOUGH_CONFORMANCE_PLUGIN_BIN")
    if bin == "" {
        t.Skip("set BOUGH_CONFORMANCE_PLUGIN_BIN to the bough-plugin-myplugin binary")
    }
    conformance.Run(t, conformance.Config{
        PluginBinary:    bin,
        Image:           "myengine:1.0",
        ReadyTimeout:    60 * time.Second,
        IdempotentCount: 2,
    })
}
```

Then in CI:

```yaml
- run: go build -o bin/bough-plugin-myplugin ./cmd/bough-plugin-myplugin
- env: { BOUGH_CONFORMANCE_PLUGIN_BIN: ${{ github.workspace }}/bin/bough-plugin-myplugin }
  run: go test -tags=conformance -race -timeout=15m ./...
```

The suite spawns your binary under go-plugin (the same path the bough
host uses in production), drives the full lifecycle twice for
idempotency, asserts the contract invariants, and runs three fault
injections (port conflict, datadir permission, image pull failure).

## What the suite checks

| Phase | Asserts |
|---|---|
| `PortRangeDefault` | returns at least one role; every range has `0 < Low < High` |
| `Up` | non-nil error on port conflict / image pull failure (skip with `SkipPortConflict` / `SkipImagePullFailure`) |
| `Up` × N | second call on already-up state is up-or-reuse, not recreate |
| `ReadyCheck` | returns true within `ReadyTimeout` |
| `EnvVars` | every value non-empty (`AssertNonEmpty`) |
| `EnvVars` | every `*_HOST` + `*_PORT` pair (including multi-port `*_<ROLE>_PORT`) and every `*_URL` is dialable from the host (`AssertReachable`) — v0.2.6 guard |
| `EnvVars` | no value contains shell metachars unless `AllowShellMetachars=true` (`AssertShellSafe`) — v0.2.5 guard |
| `EnvVars` | `Config.NativeProbe(ctx, hostPort)` returns nil for every dialable address — v0.2.6 guard, protocol-level |
| `Down` | returns nil within `GracefulTimeoutSec` |
| `Cleanup` × 2 | idempotent — second call must not error |

See [`plugins/engine/api/CONTRACT.md`](../plugins/engine/api/CONTRACT.md) for
the prose contract every invariant traces back to.

## Configuring the Run

`conformance.Config` knobs you'll touch most often:

- **`Image`** — container image ref; ends up in `extras["docker.image"]`.
- **`Extras`** — anything else the plugin reads from `UpReq.Extras`.
  `backend=docker` is injected by the suite if you don't override it.
- **`ReadyTimeout`** — how long `ReadyCheck` may poll. Defaults to 60 s;
  raise for engines with long warm-up (elasticsearch JVM ≈ 30-60 s
  cold).
- **`IdempotentCount`** — how many full lifecycle loops to run before
  the final `Cleanup`. Defaults to 2 (= one normal run + one re-Up to
  catch "already running" bugs).
- **`AllowShellMetachars`** — set true if your plugin legitimately
  emits values with `(`, `&`, `;`, `$`, etc. The mysql go-sql-driver
  DSN format is the canonical case.
- **`NativeProbe`** — a `func(ctx, hostPort) error` you supply for
  protocol-level reachability. Use `conformance.RedisPing` /
  `conformance.ElasticsearchGetRoot` if they fit; write a stdlib-only
  handshake-byte check otherwise (see
  `plugins/engine/mysql/conformance_test.go` for an example).
- **`MainPortRole`** — defaults to `"main"`. Override when your
  plugin's primary role is named differently (e.g. `"amqp"` for
  rabbitmq, `"broker"` for kafka). The suite uses it to target the
  fault tests at a single role and to label diagnostic output;
  lifecycle still iterates over every role `PortRangeDefault`
  declares.
- **`SkipPortConflict` / `SkipDatadirPermission` / `SkipImagePullFailure`**
  — opt-out individual fault cases. The bough docker plugins all set
  `SkipDatadirPermission` because they only bind-mount the datadir; the
  engine inside the container writes there. Document the reason next to
  the flag.

## Running locally

```bash
# Build the plugin binary.
go build -o bin/bough-plugin-myplugin ./cmd/bough-plugin-myplugin

# Tell the suite where it is and run.
export BOUGH_CONFORMANCE_PLUGIN_BIN=$(pwd)/bin/bough-plugin-myplugin
go test -tags=conformance -race -timeout=15m -v ./...
```

On macOS the suite talks to Docker Desktop / OrbStack / Colima
through `client.FromEnv`. On Linux it talks to the system docker
socket. The suite never depends on `docker` being on `PATH`.

## Running in CI

Use `ubuntu-24.04` (amd64) and `ubuntu-24.04-arm` (linux/arm64).
Docker daemons run on both. Don't bother with hosted darwin runners
— they cannot host the Docker daemon.

```yaml
jobs:
  conformance:
    strategy:
      fail-fast: false
      matrix:
        runner: [ubuntu-24.04, ubuntu-24.04-arm]
    runs-on: ${{ matrix.runner }}
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v5
        with: { go-version: '1.25' }
      - run: docker pull myengine:1.0
      - run: |
          mkdir -p bin
          go build -o bin/bough-plugin-myplugin ./cmd/bough-plugin-myplugin
      - env:
          BOUGH_CONFORMANCE_PLUGIN_BIN: ${{ github.workspace }}/bin/bough-plugin-myplugin
        run: go test -tags=conformance -race -timeout=15m -v ./...
```

## What the suite does NOT check

- That `bough create` correctly composes your `EnvVars` output into a
  larger `.env.local`. That's the host's job and lives in the host's
  integration tests.
- That your plugin's services-flake nix backend works. The suite
  forces `extras["backend"]="docker"`. Set `Extras["backend"]="nix"`
  on your `Config` if you want to verify the nix path separately
  (note: the bough-internal plugins do not yet — this is a follow-up).
- That your plugin builds. The suite skips with a clear message if
  `BOUGH_CONFORMANCE_PLUGIN_BIN` is unset or points at a missing
  file. Run `go build` in CI before invoking the suite.

## Multi-port engines (rabbitmq / kafka / nats / ...)

Some engines listen on more than one TCP socket. RabbitMQ exposes AMQP
on 5672 and a management UI on 15672. Kafka exposes broker + KRaft
controller. NATS exposes client + monitor + cluster. Bough models this
with one `Role` per listen point; the host allocates a deterministic
port per role and the plugin binds them all in a single `Up`.

A multi-port `Provider` differs from a single-port one only in:

1. **`PortRangeDefault`** returns one entry per role. The roles are
   plugin-defined identifiers — choose names that mean something in
   the engine's own documentation:

   ```go
   func (p *Provider) PortRangeDefault(_ context.Context) (map[string]api.PortRange, error) {
       return map[string]api.PortRange{
           "amqp":       {Low: 60000, High: 60499},
           "management": {Low: 60500, High: 60999},
       }, nil
   }
   ```

2. **`Up` iterates `req.Ports`** and binds the right container port
   to each `ps.Port`. The role label tells you which:

   ```go
   for _, ps := range req.Ports {
       switch ps.Role {
       case "amqp":
           portBindings[nat.Port("5672/tcp")] = []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: fmt.Sprint(ps.Port)}}
       case "management":
           portBindings[nat.Port("15672/tcp")] = []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: fmt.Sprint(ps.Port)}}
       }
   }
   ```

3. **`EnvVars` emits a single `_HOST`** shared by every role and a
   per-role `_<ROLE>_PORT` (and optionally `_<ROLE>_URL`):

   ```go
   func (p *Provider) EnvVars(_ context.Context, req *api.EnvVarsReq) (map[string]string, error) {
       out := map[string]string{"BOUGH_RABBITMQ_HOST": "127.0.0.1"}
       for _, ps := range req.Ports {
           role := strings.ToUpper(ps.Role)
           out["BOUGH_RABBITMQ_"+role+"_PORT"] = strconv.Itoa(ps.Port)
           out["BOUGH_RABBITMQ_"+role+"_URL"]  = fmt.Sprintf("amqp://127.0.0.1:%d", ps.Port)
       }
       return out, nil
   }
   ```

   `AssertReachable` finds `BOUGH_RABBITMQ_HOST` by walking back from
   each `_PORT` key until it hits a matching `_HOST` prefix, so the
   naming convention above lets every per-role port get dialed
   without a separate per-role `_HOST` duplicate.

4. **Set `Config.MainPortRole`** on your conformance run to the role
   the fault tests should target (the role port-conflict bind onto,
   the role `datadir-permission` and `image-pull-failure` reference
   in their error messages):

   ```go
   conformance.Run(t, conformance.Config{
       PluginBinary: bin,
       Image:        "rabbitmq:3-management",
       MainPortRole: "amqp",
       ReadyTimeout: 60 * time.Second,
   })
   ```

The conformance suite's mock plugin runs in `BOUGH_MOCK_FAIL_MODE=multi-port`
mode for `TestRun_MockPlugin_MultiPort_GreenPath` to exercise this
end-to-end without a real container — see `conformance/mock_plugin/main.go`
for a minimal reference Provider.

## Mirror the bough-internal pattern

The four bough-internal plugins (`mysql` / `postgres` / `redis` /
`elasticsearch`) each contain a single `conformance_test.go` you can
copy verbatim and tweak. Start there if you're unsure what your
plugin's test should look like.
