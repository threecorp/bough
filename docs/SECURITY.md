# bough plugin security

> Third-party engine plugins (`bough-plugin-<kind>`) are untrusted code.

## Trust model

bough discovers plugins on PATH and spawns them as subprocesses under hashicorp/go-plugin's gRPC transport. Each plugin runs with the user's full filesystem and network privileges. Today "plugin" means an engine plugin (`bough-plugin-{mysql,postgres,redis,elasticsearch}` bundled, or a third-party engine a plugin author ships) — both `bough create` (Up) and `bough remove` (Down/Cleanup) spawn one.

A malicious engine plugin could:

- read your `.git/` directory, your `.bough.yaml`, your `~/.ssh`
- read or exfiltrate whatever it's handed at `Up` time (datadir path, worktree root, port, extras)
- open network connections under your user's identity

Run only plugins you trust. See [SIGNING.md](SIGNING.md) for the (currently unenforced) signature-verification design.

## Plugin security config (schema only, not enforced)

```yaml
instinct:
  plugin_security:
    require_signed: false
    allowlist: []
    untrusted_warning: true
```

This parses (it lives under the config schema's retired `instinct:`
section — see [docs/attic/](attic/)) but nothing in the current host
reads it: no NOTICE, no allowlist check, no enforce gate runs today.
Full detail in [SIGNING.md](SIGNING.md).

## Secret redaction (current)

`internal/cli/scrub.go` truncates any observation field over 5000
chars and redacts secret-shaped tokens (API key / token / password /
Bearer-style patterns) at the point bough writes an observation to
disk — a verbatim port of ECC `observe.sh`'s redaction regex. This
runs unconditionally on every `bough hook handle` / `bough instinct observer`
write; there is no opt-out flag and no separate plugin-facing
redaction layer (there is no plugin in the loop that would need one).

## Recommended posture

- Only install engine plugin binaries (`bough-plugin-*`) you built
  yourself or trust the source of — bough does not currently verify
  them for you.
- Keep `PATH` scoped so an unrelated `bough-plugin-<kind>` binary
  from another project cannot shadow the one you intend to run.
