# bough plugin security

> Engine plugins (`bough-plugin-<kind>`) run as your user, with your
> filesystem and network access.

## Trust model

bough discovers plugins on `PATH` and spawns them as subprocesses over
hashicorp/go-plugin's gRPC transport. That covers the five bundled
plugins (`bough-plugin-{mysql,postgres,redis,elasticsearch,compose}`)
and any third-party engine. Both `bough create` (Up) and `bough remove`
(Down / Cleanup) spawn them.

A malicious engine plugin could:

- read your `.git/` directory, your `.bough.yaml`, your `~/.ssh`
- read or exfiltrate whatever it is handed at `Up` (datadir path,
  worktree root, port, extras)
- open network connections under your identity

bough does not verify plugin signatures ([SIGNING.md](SIGNING.md)).

## Recommended posture

- Install the bundled plugins from a release archive you verified
  ([SIGNING.md](SIGNING.md)), or build them yourself.
- Keep `PATH` scoped so an unrelated `bough-plugin-<kind>` binary from
  another project cannot shadow the one you intend to run.
  `bough plugins list` shows the first `bough-plugin-<kind>` file on
  `PATH` for each kind; `command -v bough-plugin-<kind>` shows the
  executable bough will actually run.
