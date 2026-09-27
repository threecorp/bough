# bough plugin template

This directory is the conformance harness for a new engine plugin: a
`conformance_test.go` and a GitHub Actions workflow that runs it. It
does not contain the plugin itself; you write that (a Go module with a
provider package and a `cmd/bough-plugin-<kind>/main.go`), and the
harness verifies it against the bough contract end-to-end.

## Steps

```bash
cp -r examples/plugin-template ../bough-plugin-cassandra
cd ../bough-plugin-cassandra
grep -rln myplugin | xargs sed -i.bak 's/myplugin/cassandra/g; s/MyPlugin/Cassandra/g'
find . -name '*.bak' -delete
```

Then:

1. **The provider** — implement `plugins/engine/api.EngineProvider`
   (`PortRangeDefault`, `Up`, `ReadyCheck`, `EnvVars`, `Down`,
   `Cleanup`). `plugins/engine/{mysql,postgres,redis,elasticsearch}/`
   are the reference implementations.
2. **`cmd/bough-plugin-<kind>/main.go`** — the go-plugin server entry.
   Copy `cmd/bough-plugin-mysql/main.go` and change the imported
   provider package.
3. **`conformance_test.go`** — resolve its `TODO:` markers: the `Image`,
   the `ReadyTimeout` for the engine's cold start, and a `NativeProbe`
   if the stdlib helpers (`RedisPing`, `ElasticsearchGetRoot`) don't fit.
   See the `mysql` plugin for a stdlib-only handshake-byte probe.
4. **`.github/workflows/ci.yml`** — change the pre-pull image ref.

Once those are in place:

```bash
go build -o bin/bough-plugin-cassandra ./cmd/bough-plugin-cassandra
docker pull cassandra:5.0
BOUGH_CONFORMANCE_PLUGIN_BIN=$(pwd)/bin/bough-plugin-cassandra \
  go test -tags=conformance -race -timeout=15m -v ./...
```

## Multi-port engines

Engines that listen on more than one TCP socket (rabbitmq AMQP +
Management, kafka broker + KRaft controller, NATS client + monitor +
cluster) declare one entry per role from `PortRangeDefault` and bind
every entry of `UpReq.Ports` in their `Up` body. Set the matching
`MainPortRole` on the conformance config so the fault tests target
the right role:

```go
conformance.Run(t, conformance.Config{
    PluginBinary: bin,
    Image:        "rabbitmq:3-management",
    MainPortRole: "amqp",          // matches PortRangeDefault["amqp"]
    ReadyTimeout: 60 * time.Second,
})
```

See [`docs/PLUGIN_AUTHOR_GUIDE.md` — Multi-port engines](../../docs/PLUGIN_AUTHOR_GUIDE.md#multi-port-engines-rabbitmq--kafka--nats--)
for the rabbitmq author's view of `PortRangeDefault`, `Up`, and
`EnvVars` shapes.

## Background reading

- [`plugins/engine/api/CONTRACT.md`](../../plugins/engine/api/CONTRACT.md) —
  the bough plugin contract every conformance assertion traces back to.
- [`docs/PLUGIN_AUTHOR_GUIDE.md`](../../docs/PLUGIN_AUTHOR_GUIDE.md) —
  how the conformance suite ergonomics work end-to-end.
- The bough-internal plugins under `plugins/engine/` — copy
  whichever one is closest to your engine.
