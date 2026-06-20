package schema

import (
	"crypto/sha256"
	"encoding/hex"
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
	// v0.5 (= shipped 2026-06-20, unchanged for backwards compat).
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

	// v0.6 additions (= round 4 priority A3, AI #1 + #2). The wire
	// proto stays at v0.5 (= 17 fields) — these groups round-trip
	// through Payload so the v0.5 plugin contract is unbroken. v0.7
	// graduates them to proto fields alongside the MemoryBackend v2
	// bump.
	Target     Target     // compile target (format + host + mcp kind)
	Invocation Invocation // trigger + contraindications + env / bin deps
	Contract   Contract   // inputs / outputs / side effects / state-changing
	Validation Validation // probes + test commands + expected signals
	Provenance Provenance // instinct ids + tree sha + generated_by
	Checksum   string     // sha256 hex of canonical bytes; round 4 AI #1 idempotency token
}

// Target identifies the compile target shape (what `bough capability
// compile` produces) and where it runs.
type Target struct {
	Format  string `json:"format"`             // "agent-skill" | "claude-skill" | "mcp-tool" | "mcp-resource" | "mcp-prompt" | "json"
	Host    string `json:"host,omitempty"`     // "claude-code" | "github-copilot" | "cursor" | "codex" | "gemini-cli" | "generic"
	MCPKind string `json:"mcp_kind,omitempty"` // "tool" | "resource" | "prompt" (= when Format starts with "mcp-")
}

// Invocation describes the conditions under which the artifact
// fires and what shell prerequisites it needs.
type Invocation struct {
	Trigger           string   `json:"trigger,omitempty"`
	Contraindications []string `json:"contraindications,omitempty"`
	RequiredEnv       []string `json:"required_env,omitempty"`
	RequiredBins      []string `json:"required_bins,omitempty"`
}

// Contract pins the artifact's input / output expectations and
// whether invocation has visible side effects.
type Contract struct {
	Inputs        []string `json:"inputs,omitempty"`
	Outputs       []string `json:"outputs,omitempty"`
	SideEffects   []string `json:"side_effects,omitempty"`
	StateChanging bool     `json:"state_changing,omitempty"`
}

// Validation gates whether the artifact is safe to publish.
type Validation struct {
	Probes          []string `json:"probes,omitempty"`
	TestCommands    []string `json:"test_commands,omitempty"`
	ExpectedSignals []string `json:"expected_signals,omitempty"`
}

// Provenance is the supply-chain footprint round 4 AI #2 + round 3
// priority B both insisted CapabilityArtifact must carry from v0.6.
// SourceRef + TreeSHA let an artifact be verified against the
// originating git state; EvidenceFingerprints map back to the
// original TraceBundles.
type Provenance struct {
	InstinctIDs          []string `json:"instinct_ids,omitempty"`
	TraceBundleIDs       []string `json:"trace_bundle_ids,omitempty"`
	EvidenceFingerprints []string `json:"evidence_fingerprints,omitempty"`
	SourceRef            string   `json:"source_ref,omitempty"`  // git sha / tag
	TreeSHA              string   `json:"tree_sha,omitempty"`    // sha256 of repo tree at compile time
	GeneratedBy          string   `json:"generated_by,omitempty"` // e.g. "bough@v0.6.0"
}

// canonicalSnapshot is the deterministic field set CanonicalBytes
// serialises. Stability / CreatedAt / Checksum are excluded so the
// same logical artifact regenerated tomorrow yields the same
// checksum.
type canonicalSnapshot struct {
	ID                  string                 `json:"id"`
	Kind                CapabilityArtifactKind `json:"kind"`
	Name                string                 `json:"name"`
	Description         string                 `json:"description"`
	InvocationCondition string                 `json:"invocation_condition"`
	Inputs              []string               `json:"inputs"`
	Outputs             []string               `json:"outputs"`
	Steps               []string               `json:"steps"`
	Constraints         []string               `json:"constraints"`
	EvidenceRefs        []string               `json:"evidence_refs"`
	Confidence          float64                `json:"confidence"`
	Version             string                 `json:"version"`
	SourceInstincts     []string               `json:"source_instincts"`
	Scope               Scope                  `json:"scope"`
	Target              Target                 `json:"target"`
	Invocation          Invocation             `json:"invocation"`
	Contract            Contract               `json:"contract"`
	Validation          Validation             `json:"validation"`
	Provenance          Provenance             `json:"provenance"`
}

// CanonicalBytes returns the deterministic JSON bytes Checksum
// hashes. Marshal errors are impossible against in-memory data
// structures with json tags, so the implementation discards the
// error and returns a zero-length slice on the unreachable path.
func (a *CapabilityArtifact) CanonicalBytes() []byte {
	snapshot := canonicalSnapshot{
		ID:                  a.ID,
		Kind:                a.Kind,
		Name:                a.Name,
		Description:         a.Description,
		InvocationCondition: a.InvocationCondition,
		Inputs:              a.Inputs,
		Outputs:             a.Outputs,
		Steps:               a.Steps,
		Constraints:         a.Constraints,
		EvidenceRefs:        a.EvidenceRefs,
		Confidence:          a.Confidence,
		Version:             a.Version,
		SourceInstincts:     a.SourceInstincts,
		Scope:               a.Scope,
		Target:              a.Target,
		Invocation:          a.Invocation,
		Contract:            a.Contract,
		Validation:          a.Validation,
		Provenance:          a.Provenance,
	}
	raw, _ := json.Marshal(snapshot)
	return raw
}

// ComputeChecksum sets a.Checksum to sha256(CanonicalBytes) hex and
// returns the computed value for caller convenience. The CLI uses
// the return value to short-circuit Compile when the existing
// artifact on disk already matches the hash (= no-op skip).
func (a *CapabilityArtifact) ComputeChecksum() string {
	sum := sha256.Sum256(a.CanonicalBytes())
	a.Checksum = hex.EncodeToString(sum[:])
	return a.Checksum
}
