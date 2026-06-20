# bough-plugin-memory-template

Minimal working template for a v0.5 `bough-plugin-memory-<name>` binary.

## Quick start

```sh
cp -r memory-plugin-template/ ~/my-plugin/
cd ~/my-plugin/
# Replace each templateBackend method body with your backend's call.
go build -o dist/bough-plugin-memory-mine ./
BOUGH_CONFORMANCE_MEMORY_PLUGIN_BIN=$PWD/dist/bough-plugin-memory-mine \
    go test -tags=conformance ./...
```

## What to read

1. `plugins/memory/api/CONTRACT.md` — the wire contract you implement.
2. `plugins/memory/api/interface.go` — the Go-side surface.
3. `plugins/memory/sqlite/sqlite.go` — the bundled reference. Read for shape.
4. `docs/MEMORY_PLUGIN_AUTHOR_GUIDE.md` — step-by-step for the full ship cycle.

## What to do

The template's `Store` method returns an error so the conformance
suite fails until you replace it with a real implementation. The
other six methods return zero values so you can run the suite
incrementally as you fill them in.

The conformance suite (`conformance/memory/`) covers:

- Lifecycle: Health → Capabilities → Store → Query → Forget → Export → Import.
- Bloat: MaxResults + MaxTokens hard limits.
- Concurrency: parallel Store + Query (round 3 AI #3 invariant).

Pass all three before publishing.
