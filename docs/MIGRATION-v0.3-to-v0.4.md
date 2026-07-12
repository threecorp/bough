# bough v0.3.x → v0.4.0 migration guide

v0.4.0 generalises bough from "DB plugin orchestrator" to "engine plugin
orchestrator" so middleware (rabbitmq, kafka, nats, minio, ...) can be
added as plugins on the same lifecycle as the existing DB plugins. The
breaking changes are collected into one release so plugin authors pay
the cost once.

This guide covers:

1. What changed on the wire and on disk
2. What `bough` reads as a fallback during v0.4.x (no immediate action
   needed for existing deployments)
3. What needs editing before v0.5.0 (when the fallbacks are removed)

## TL;DR

**v0.5.0 already shipped and removed the v0.3.x fallback described
below.** If you are still running a `.worktree-isolation.yaml` config
(or a plugin binary built before v0.4.0), migrate to `.bough.yaml`
before upgrading past v0.4.x — a current bough host no longer reads
the old file, section names, field names, or gRPC handshake key at
all; see Timeline below.

(Historical, for anyone running a pinned v0.4.x host: during that
window your existing `.worktree-isolation.yaml` kept working without
edits — the host loader detected the old file and read it with a
deprecation warning. That grace period ended at v0.5.0.)

## What renamed

| Layer | v0.3.x | v0.4.0 | v0.4.x fallback |
|---|---|---|---|
| YAML file | `.worktree-isolation.yaml` | `.bough.yaml` | reads old name when new is absent |
| registry file | `.worktree-ports.json` | `.bough-ports.json` | same |
| backup dir | `~/.claude/backups/` | `~/.bough/backups/` | same |
| YAML section | `databases:` | `engines:` | loader accepts both |
| YAML field | `initial_databases: ["auba"]` | `initial_resources: [{type: database, name: auba}]` | old `[]string` auto-converts to `[{type: database, name: <s>}]` |
| YAML field | `port_range: [42000, 44999]` | `port_ranges: { main: [42000, 44999] }` | old array auto-wraps as `{main: [...]}` |
| Go interface | `DBProvider` | `EngineProvider` | n/a (host ↔ plugin gRPC) |
| Go pkg | `plugins/db/` | `plugins/engine/` | `git mv`; external plugin authors must update `import` paths |
| gRPC handshake | `BOUGH_DB_PLUGIN` | `BOUGH_ENGINE_PLUGIN` | host attempts new key first, falls back to old (v0.4.x only) |
| Plugin binary | `bough-plugin-<kind>` | unchanged | n/a |
| Single-port EnvVars | `BOUGH_<ENGINE>_HOST/_PORT/_URL` | unchanged | mysql/redis/postgres/es keep the role-omitted form |
| Multi-port EnvVars | n/a | `BOUGH_<ENGINE>_<ROLE>_PORT/_URL` (`HOST` stays shared) | rabbitmq/kafka/nats only |

## YAML example, side by side

v0.3.x:
```yaml
schema_version: 1
databases:
  - kind: mysql
    port_range: [42000, 44999]
    initial_databases: ["auba"]
```

v0.4.0:
```yaml
schema_version: 2
engines:
  - kind: mysql
    port_ranges:
      main: [42000, 44999]
    initial_resources:
      - { type: database, name: auba }

  # multi-port engine — plugin lands in v0.5.0; schema is ready in v0.4.0
  - kind: rabbitmq
    port_ranges:
      amqp:       [60000, 60499]
      management: [60500, 60999]
    initial_resources:
      - { type: vhost, name: dev }
```

## What plugin authors need to do

External plugin maintainers (your `bough-plugin-<kind>` repo):

1. Update the Go module import from
   `github.com/ikeikeikeike/bough/plugins/db/api` to
   `github.com/ikeikeikeike/bough/plugins/engine/api`.
2. Rename your provider's signature to match `EngineProvider`:
   `Up(ctx, *UpReq)` now takes `[]PortSpec` instead of a single `Port`
   field, `[]ResourceSpec` instead of `InitialDatabases []string`.
   The bundled `pickMainPort()` and `pickFirstResourceName()` helpers
   in `plugins/engine/api/shims.go` cover the trivial single-port case.
3. Rebuild your binary against bough v0.4.0 — the magic-cookie handshake
   has moved, but the host's fallback path keeps v0.3.x binaries
   working through v0.4.x.

## What's NOT removed in v0.4.0

- `Provision()` method — there is none, intentionally. Engine-specific
  provisioning (kafka topic creation, minio bucket init) lives inside
  `Up`'s contract. If you need an out-of-band step, declare it through
  a capability gate in your plugin.
- Single-port engines do **not** need a role declaration. `Role: ""`
  and `Role: "main"` are equivalent on the wire.
- Plugin binary names (`bough-plugin-<kind>`) stay identical.

## Timeline

- **v0.4.0** — new schema is canonical, old schema is still read with a
  deprecation warning.
- **v0.4.x** — every minor release prints the same deprecation warnings;
  no behavioural change.
- **v0.5.0** — old YAML file name, old section name, old field names,
  and old magic-cookie fallback are all removed. Plugins that have not
  rebuilt against v0.4.0+ stop loading.

If you are unsure how to migrate, open an issue at
https://github.com/threecorp/bough/issues with your current
`.worktree-isolation.yaml` and we'll convert it.
