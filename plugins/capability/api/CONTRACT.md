# CapabilityCompiler plugin contract (v0.5 freeze, v0.6 implementation)

v0.5 freezes the wire and Go contract for
`bough-plugin-capability-<name>` plugins WITHOUT shipping a working
implementation. The host's `bough capability compile` CLI and the
first official compilers (SkillX, Anything2Skill, Alita-G —
all v0.6.x experimental) land in v0.6.0.

## Why freeze in v0.5 if we won't implement until v0.6

Plugin authors can prototype against a stable contract while v0.5
is shipping. The `CapabilityArtifact` schema in `pkg/schema` lands
in v0.5 alongside this contract so the data structures and wire
format move together. v0.5 docs (`CAPABILITY_COMPILER_PREVIEW.md`)
show concrete message shapes rather than vapourware.

## Handshake (reserved for v0.6)

```
ProtocolVersion: 1
MagicCookieKey:  BOUGH_CAPABILITY_PLUGIN
MagicCookieValue: v1
PluginKey: capability_compiler
```

The cookie is registered now to prevent name collisions with
unrelated plugin types and to let v0.6 host code start discovering
without renegotiating cookies.

## The one RPC

`Compile(CompileReq) → CompileResp`

Take an approved set of instincts (referenced by ID; the host
owns lookup) and emit `CapabilityArtifact` candidates of the
requested `target_kinds`. `DryRun=true` asks the plugin to return
artifacts without committing any side effects — relevant when a
future compiler runs an LLM with non-zero spend per invocation.

`CapabilityArtifact` carries twelve minimal fields (see
`pkg/schema/capability.go`) plus a `Payload []byte` json escape
hatch so v0.6+ compilers (Anything2Skill, gh skill format, MCP
tools/resources/prompts) can carry their richer metadata without a
wire bump.

## Supply chain (round 3 AI #3)

Skills, MCP tools, and other compiled artifacts have a non-trivial
supply-chain attack surface (see the 2026 SKILL.md research). v0.6
compilers SHOULD:

- compute and emit `source_ref` / `tree_sha` provenance metadata in
  `Payload`;
- accept a `--pin-source` flag from the host to refuse compilation
  unless source pinning is satisfied;
- treat third-party `payload_json` from a Mint chain as untrusted
  input — never `exec` strings from instincts.

The host's plugin signing policy (`instinct.plugin_security`) gates
which capability plugins it discovers. v0.5 is warn-only; v0.6
adds an enforce option.
