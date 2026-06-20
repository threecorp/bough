package schema

// Stability annotates a schema value with the producer-side
// guarantee about its shape. Round 3 of the external review made
// this explicit so consumers of v0.5 artifacts know which fields
// are committed (= the minimal shape this package declares) and
// which are still in flight for v0.6.
//
// v0.5 every CapabilityArtifact is StabilityExperimental: the
// minimal field set (Name, Description, InvocationCondition,
// Inputs, Outputs, Steps, Constraints, EvidenceRefs, Confidence,
// Version, SourceInstincts) is committed, but v0.6 will add
// target_format, target_host, provenance metadata, validation
// probes, and supply-chain tree_sha as additive fields. Consumers
// reading a v0.5 artifact should treat unknown fields as absent
// rather than failing the parse.
type Stability string

const (
	StabilityUnspecified  Stability = ""
	StabilityStable       Stability = "stable"
	StabilityPreview      Stability = "preview"
	StabilityExperimental Stability = "experimental"
)
