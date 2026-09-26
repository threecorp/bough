# bough plugin security

> Third-party engine plugins (`bough-plugin-<kind>`) are untrusted code.

## Trust model

bough discovers plugins on PATH and spawns them as subprocesses under hashicorp/go-plugin's gRPC transport. Each plugin runs with the user's full filesystem and network privileges. Today "plugin" means an engine plugin (`bough-plugin-{mysql,postgres,redis,elasticsearch}` bundled, or a third-party engine a plugin author ships) — both `bough create` (Up) and `bough remove` (Down/Cleanup) spawn one.

A malicious engine plugin could:

- read your `.git/` directory, your `.bough.yaml`, your `~/.ssh`
- read or exfiltrate whatever it's handed at `Up` time (datadir path, worktree root, port, extras)
- open network connections under your user's identity

Run only plugins you trust. See [SIGNING.md](SIGNING.md) for the (currently unenforced) signature-verification design.

## Plugin security config

There is none today. The signature-verification design in
[SIGNING.md](SIGNING.md) has no config surface: the schema it used to
carry lived under the `instinct:` section, which was removed in v0.28.0
along with the rest of the continuous-learning loop. Nothing read it —
no NOTICE, no allowlist check, no enforce gate — so it went with the
section rather than being rehomed to a key that would also do nothing.
Wiring enforcement is what earns a config key back.

## Recommended posture

- Only install engine plugin binaries (`bough-plugin-*`) you built
  yourself or trust the source of — bough does not currently verify
  them for you.
- Keep `PATH` scoped so an unrelated `bough-plugin-<kind>` binary
  from another project cannot shadow the one you intend to run.
