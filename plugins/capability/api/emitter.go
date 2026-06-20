package api

import (
	"context"

	"github.com/ikeikeikeike/bough/pkg/schema"
)

// This file lifts the Emitter contract out into plugins/capability/
// api/ so v0.6.x can graduate emitters into gRPC plugin slots
// without rewriting the registry. v0.6.0 keeps the implementations
// inside the bough core (internal/capability/ + internal/export/),
// but the *interface* lives here so a future
// `bough-plugin-capability-emitter-skillx` binary can satisfy the
// same shape over the wire (round 4 priority A13).

// EmitOptions tunes how an Emitter renders an artifact. Host pins
// the agent runtime layout (claude-code vs github-copilot vs the
// generic shape); OutDir, when non-empty, signals that the emitter
// should write directly to disk and leave Bytes empty.
type EmitOptions struct {
	Host    string // "claude-code" | "github-copilot" | "cursor" | "codex" | "gemini-cli" | "generic"
	OutDir  string
	Verbose bool
}

// EmitResult is the byte representation of one rendered artifact
// plus the suggested filename a CLI would write it to. ContentType
// is informational (text/markdown, application/json) so the CLI
// can pick the right extension when OutDir is empty.
type EmitResult struct {
	Filename    string
	ContentType string
	Bytes       []byte
}

// Emitter renders a CapabilityArtifact into a target-specific shape
// (agent-skill / claude-skill / mcp-tool / mcp-resource / mcp-prompt
// / json / ...).
//
// Format() returns the canonical name the registry and CLI use.
// Implementations should return the same string they were registered
// under so the lookup is round-trip safe.
//
// Emit() takes the artifact + options and returns either bytes (when
// opts.OutDir == "") or writes to disk and returns the filename (when
// opts.OutDir != ""). The host treats a nil EmitResult as a successful
// no-op so emitters can skip artifacts whose Kind they do not handle.
type Emitter interface {
	Format() string
	Emit(ctx context.Context, artifact schema.CapabilityArtifact, opts EmitOptions) (*EmitResult, error)
}
