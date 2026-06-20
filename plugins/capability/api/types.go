package api

import "time"

// Stability mirrors pkg/schema.Stability. v0.5 marks every
// CapabilityArtifact as Experimental so consumers know the shape may
// grow optional fields in v0.6 without a wire break.
type Stability string

const (
	StabilityStable       Stability = "stable"
	StabilityPreview      Stability = "preview"
	StabilityExperimental Stability = "experimental"
)

// CapabilityArtifactKind enumerates the eight derivation targets the
// bough vision (post round 2.5 / round 3 review) treats as first-
// class. v0.5 only persists artifacts of these kinds; v0.6+
// compilers are not asked to invent new kinds.
type CapabilityArtifactKind string

const (
	ArtifactMemory    CapabilityArtifactKind = "memory"
	ArtifactRule      CapabilityArtifactKind = "rule"
	ArtifactSkill     CapabilityArtifactKind = "skill"
	ArtifactCommand   CapabilityArtifactKind = "command"
	ArtifactWorkflow  CapabilityArtifactKind = "workflow"
	ArtifactTool      CapabilityArtifactKind = "tool"
	ArtifactAgent     CapabilityArtifactKind = "agent"
	ArtifactEvaluator CapabilityArtifactKind = "evaluator"
)

// Scope mirrors plugins/memory/api.Scope; duplicated so this contract
// is independently complete.
type Scope struct {
	Level      string
	WorktreeID string
	RepoName   string
}

// CapabilityArtifact is the compiled output. Payload is a free-form
// JSON byte slot so v0.6+ compilers (Anything2Skill, gh skill format,
// MCP tools/resources/prompts) can carry their richer metadata
// without a wire bump. The host treats Payload as opaque on v0.5 —
// it is round-tripped through memory Export/Import but not parsed.
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
	Payload             []byte // free-form JSON; plugin-defined
}

// EvidencePolicy gates how aggressive a compiler is allowed to be.
// RequireMinTraces=3 keeps single-shot anecdotes from being promoted;
// AllowedSources whitelists which trace sources count toward the
// minimum.
type EvidencePolicy struct {
	RequireMinTraces int
	AllowedSources   []string
}

type CompileReq struct {
	SourceInstinctIDs []string
	TargetKinds       []CapabilityArtifactKind
	Scope             Scope
	EvidencePolicy    EvidencePolicy
	RequireApproval   bool
	DryRun            bool
}
type CompileResp struct {
	Candidates []*CapabilityArtifact
}
