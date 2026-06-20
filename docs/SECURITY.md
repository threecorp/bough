# bough plugin security

> Third-party memory / instinct / capability plugins are untrusted code.

## Trust model

bough discovers plugins on PATH and spawns them as subprocesses under hashicorp/go-plugin's gRPC transport. Each plugin runs with the user's full filesystem and network privileges. The plugin signing surface is **warn-only on v0.5**; an enforce option lands in v0.6.

A malicious plugin could:

- read your `.git/` directory, your `.bough.yaml`, your `~/.ssh`
- exfiltrate the instincts you store (rules, evidence references)
- emit prompt-injection content into `events.jsonl` (round 3 AI #3 references the 2026 SKILL.md attack surface research)
- decline to honour the v0.5 redaction contract on payloads it persists

Run only plugins you trust. The v0.5 host emits a NOTICE the first time a non-allowlisted plugin is spawned.

## `instinct.plugin_security` config

```yaml
instinct:
  plugin_security:
    require_signed: false        # v0.5: warn-only; v0.6: enforce option
    allowlist:
      - "bough-plugin-memory-sqlite"   # the bundled SQLite reference
      - "bough-plugin-memory-mem0"     # the v0.6 official mem0 adapter
    untrusted_warning: true       # print a banner when a non-allowlist plugin spawns
```

## Skill / capability export risk

v0.6 ships `bough capability compile` and `bough capability export` for Claude Skills / Agent Skills / MCP. Compiled artifacts can carry executable steps (commands, tool invocations); a compromised compiler or a poisoned source instinct could produce a SKILL.md that contains hidden instructions or malicious shell.

Mitigations (planned for v0.6, designed for in v0.5):

- `--pin-source <ref>` / `source_ref` / `tree_sha` metadata so a downstream consumer can verify the artifact came from a known commit.
- Provenance bytes inside `CapabilityArtifact.Payload`: the producer plugin records the trace bundle IDs and evidence fingerprints that fed the compilation.
- Plugin signing enforcement to refuse compilation from a non-signed plugin.

## PII / secret redaction

The host's `Redactor` (see `internal/instinct/redaction.go`) strips email / api_key / token / password / aws_secret patterns from raw observer content before any minter sees it. Round 1 + round 3 of external review treated PII leak via session logs as the round 3 amplifier on top of memory poisoning.

`instinct.mint.redaction.enabled: true` (the default) is the supported configuration. If you turn it off, secrets observed in stdin or in a session log land in the backend verbatim.

## Recommended posture

For solo development with the SQLite reference-fallback only:

- `require_signed: false`
- `allowlist: []`
- `untrusted_warning: true`

For a team install with mem0 official plugin (v0.6+):

- `require_signed: true`
- `allowlist: ["bough-plugin-memory-mem0"]`
- `untrusted_warning: true`

For an enterprise install once Graphiti and capability compilers ship (v0.6.x):

- Build plugin binaries from a known commit in your own CI.
- Pin the SHA in `allowlist` (v0.6 syntax: `bough-plugin-memory-mem0@<sha>`).
- Mirror the binaries in your artifact registry; do not pull from upstream releases at install time.
