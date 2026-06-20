package schema

import (
	"encoding/json"
	"time"
)

// CapabilityArtifactKind enumerates the eight first-class derivation
// targets the bough vision treats as artifact kinds. The set was
// finalised in round 2.5 / round 3 of external review: every kind a
// future CapabilityCompiler emits MUST be one of these eight, so
// downstream consumers (Claude Skills exporter, MCP exporter, gh
// skill bridge) can switch on Kind without surprise.
type CapabilityArtifactKind string

const (
	ArtifactKindUnspecified CapabilityArtifactKind = ""
	ArtifactKindMemory      CapabilityArtifactKind = "memory"    // a memory entry, e.g. "stash this fact about the repo"
	ArtifactKindRule        CapabilityArtifactKind = "rule"      // a behavioural rule, e.g. lint-style guideline
	ArtifactKindSkill       CapabilityArtifactKind = "skill"     // Anthropic Agent Skills / Claude Skills shaped artifact
	ArtifactKindCommand     CapabilityArtifactKind = "command"   // a deterministic CLI / shell command
	ArtifactKindWorkflow    CapabilityArtifactKind = "workflow"  // a stepwise workflow specification
	ArtifactKindTool        CapabilityArtifactKind = "tool"      // an MCP tool / external API tool
	ArtifactKindAgent       CapabilityArtifactKind = "agent"     // a sub-agent specification
	ArtifactKindEvaluator   CapabilityArtifactKind = "evaluator" // a scorer / evaluator specification
)

// CapabilityArtifact is what a v0.6+ CapabilityCompiler emits and
// what a v0.6+ exporter renders into Claude Skills / Agent Skills /
// MCP form. v0.5 commits the minimal schema (Round 3 AI #1's twelve
// fields plus Payload as an escape hatch); v0.6 will add optional
// target_format, target_host, validation probes, and supply-chain
// tree_sha as additive fields.
//
// Stability is always StabilityExperimental on v0.5; consumers
// reading a v0.5 artifact should treat unknown fields they
// encounter in v0.6+ artifacts as additive and tolerate them.
//
// Payload is the v0.5 escape hatch. v0.6+ compilers store their
// richer metadata here as plugin-defined JSON; the host treats
// Payload as opaque (round-trips it through Export / Import but
// does not parse). The SQLite reference-fallback schema reserves a
// `metadata TEXT` column for exactly this.
type CapabilityArtifact struct {
	Stability           Stability
	ID                  string
	Kind                CapabilityArtifactKind
	Name                string
	Description         string
	InvocationCondition string
	Inputs              []string
	Outputs             []string
	Steps               []string
	Constraints         []string
	EvidenceRefs        []string
	Confidence          float64
	Version             string
	SourceInstincts     []string
	Scope               Scope
	CreatedAt           time.Time
	Payload             json.RawMessage
}
