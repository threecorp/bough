// Package schema declares the canonical Go types that flow through
// the bough host's instinct subsystem: observer → redaction →
// poisoning_guard → coordinator → memory plugin.
//
// These are the host-side / coordinator-side representations. The
// plugin-author-facing types live next to each interface in
// plugins/memory/api/types.go, plugins/instinct/api/types.go,
// plugins/capability/api/types.go, and plugins/evaluator/api/types.go.
// The gRPC server / client adapters in those packages convert
// between schema.Foo (Go-idiomatic, time.Time, json.RawMessage) and
// the api.Foo wire forms (int64 unix epoch, []byte).
//
// Stability: every type defined here is marked Experimental on v0.5.
// The shapes WILL grow optional fields in v0.6 (MCP export metadata,
// target_format / target_host on CapabilityArtifact, supply-chain
// tree_sha, ...). Consumers should not rely on the field set being
// exhaustive; instead use the Stability annotation on a value to
// detect whether the producer was a v0.5 or v0.6+ component.
//
// The split between this package and the plugin api/ packages is
// deliberate. Plugin authors who do not want to import a bough-
// internal package can build against api/types.go alone — those
// types are stable on the wire even when this package's Go-level
// shapes change. Coordinator code uses schema.Foo so the rest of
// the host enjoys time.Time / json.RawMessage / typed enums instead
// of int64 epochs and raw byte slices.
package schema
