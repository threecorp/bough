// Package api freezes the v0.5.0 Go contract for bough capability
// compiler plugins WITHOUT shipping a working implementation. v0.5
// only commits the wire format (api/proto/capability.proto), the Go
// interface, and the request/response types; the host's
// `bough capability compile` CLI and the first official compilers
// (SkillX, Anything2Skill, Alita-G — all v0.6.x experimental) land
// in v0.6.0.
//
// Why freeze the contract in v0.5 if we will not implement until v0.6:
// plugin authors can prototype against a stable wire and Go contract
// while v0.5 is shipping. The CapabilityArtifact schema in
// pkg/schema lands in v0.5 alongside this contract so the data
// structures and the wire format move together. v0.5 docs
// (CAPABILITY_COMPILER_PREVIEW.md) show concrete message shapes
// rather than vapourware.
//
// See docs/SECURITY.md for the supply-chain attack surface of
// SKILL.md-style artifacts; v0.6 compilers should respect plugin
// signing policies before emitting executable steps.
package api

import "context"

// CapabilityCompiler turns an approved set of instincts (referenced
// by ID; the host owns lookup) into one or more CapabilityArtifacts
// that v0.6+ exporters render as Claude Skills, Agent Skills, MCP
// tools/resources/prompts, raw markdown, or JSON.
//
// v0.5 ships this interface as STUB: no plugin will be discovered
// for "capability" kind, and the host will respond with an
// implementation-pending notice if `bough capability compile` is
// invoked. The Go interface and types are committed so the host's
// pluginhost can discover capability plugins as soon as v0.6+
// implementations exist.
type CapabilityCompiler interface {
	Compile(ctx context.Context, req *CompileReq) (*CompileResp, error)
}
