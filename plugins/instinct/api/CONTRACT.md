# InstinctMinter plugin contract (v0.5)

The InstinctMinter plugin contract is the wire and Go contract
between the bough host and any `bough-plugin-instinct-<name>`
binary that mints `InstinctCandidate` rows from raw `TraceBundle`s.

Bough ships a default in-process minter
(`internal/instinct.builtin_minter`) so this contract is only
needed when a v0.6+ adapter wires an LLM-backed candidate
generator (SkillX, Anything2Skill, MUSE-Autoskill, ...) into the
trace pipeline.

## Why the split from MemoryBackend

Memory backends (mem0, Graphiti) are good at storage and
retrieval, not at the bough-specific concern of distilling a
per-worktree trace into a behavioural rule. Forcing every memory
backend to implement `Mint` would push bough's trace-handling
logic into every plugin author's lap. Isolating it here keeps
MemoryBackend authors focused on persistence.

## Handshake

```
ProtocolVersion: 1
MagicCookieKey:  BOUGH_INSTINCT_PLUGIN
MagicCookieValue: v1
PluginKey: instinct_minter
```

## The one RPC

`Mint(MintReq) → MintResp`

Inspect the bundle of traces in `TraceBundles` and return zero or
more candidates. Returning an empty `Candidates` list is fine when
the bundle does not carry enough signal — the host's poisoning
guard handles dedupe (sha256 over `rule + scope`) and budget/
poisoning enforcement; minters focus on extraction quality, not on
whether a candidate "deserves" to be stored.

`Candidates` is plural by design: one trace bundle may yield
multiple candidates (e.g., a session log surfacing two separate
behavioural rules).

## Author checklist

Same shape as `plugins/memory/api/CONTRACT.md`:

1. Implement `InstinctMinter` (1 method) in Go.
2. Cross-build as `bough-plugin-instinct-<name>`.
3. Drop a conformance test against `conformance/instinct`:

   ```go
   //go:build conformance

   package <yourplugin>

   import (
       "testing"

       instconf "github.com/ikeikeikeike/bough/conformance/instinct"
   )

   func TestConformance(t *testing.T) {
       instconf.Run(t, instconf.Config{
           Plugin:  "<name>",
           Datadir: t.TempDir(),
       })
   }
   ```
