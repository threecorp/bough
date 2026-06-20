package capability

import (
	"context"
	"fmt"
	"time"

	"github.com/ikeikeikeike/bough/pkg/schema"
	capapi "github.com/ikeikeikeike/bough/plugins/capability/api"
)

// Compiler orchestrates instinct → artifact synthesis. v0.6 ships
// a builtin compiler: walk an approved set of instincts, construct
// a CapabilityArtifact per (target_kind, target_format) pair, run
// the Provenance + Validation steps that round 4 priority A3 made
// first-class, and dispatch each artifact through the Emitter
// registry.
//
// Round 4 AI #2 deferred the plugin-slot version of this to v0.6.x;
// v0.6 keeps the registry internal and exposes only the
// CapabilityCompiler stub over gRPC for upstream callers (the
// official capability plugin contract is frozen — see
// plugins/capability/api/interface.go).
type Compiler struct {
	registry *Registry
}

// NewCompiler returns a Compiler that dispatches through the given
// registry. Tests can pass a hand-rolled registry; production
// callers use NewCompiler(NewRegistry()) and Register their
// emitters before the first Compile call.
func NewCompiler(registry *Registry) *Compiler {
	return &Compiler{registry: registry}
}

// CompileRequest is the host-side mirror of capapi.CompileReq with
// the additional Targets field that round 4 priority A3 splits out
// from Kind. Where Kind says "what" (skill / tool / workflow / ...),
// Target.Format says "in what shape" (agent-skill / mcp-tool / ...).
type CompileRequest struct {
	SourceInstincts []schema.Instinct
	TargetKinds     []schema.CapabilityArtifactKind
	Targets         []schema.Target
	Scope           schema.Scope
	EvidencePolicy  capapi.EvidencePolicy
	RequireApproval bool
	DryRun          bool
}

// CompileResult bundles the synthesised artifacts with the emitter
// outputs so the CLI can both persist the artifacts (for round-trip
// via Export / Import) and write the rendered files.
type CompileResult struct {
	Artifacts []schema.CapabilityArtifact
	Emissions []capapi.EmitResult
}

// Compile is the host orchestrator's entry. The dispatch loop:
//
//	for instinct in req.SourceInstincts:
//	    for kind in req.TargetKinds:
//	        artifact = synthesise(instinct, kind)
//	        for target in req.Targets:
//	            artifact.Target = target
//	            artifact.ComputeChecksum()        # round 4 AI #1 idempotency
//	            result.Artifacts.append(artifact)
//	            if !DryRun:
//	                emitter = registry.Lookup(target.Format)
//	                emission = emitter.Emit(ctx, artifact, opts)
//	                result.Emissions.append(emission)
//
// The synthesiser is intentionally a one-line "rule wrapped in an
// artifact" mapping; v0.6.x replaces it with the SkillX /
// Anything2Skill / Alita-G compilers without touching this dispatch
// path.
func (c *Compiler) Compile(ctx context.Context, req *CompileRequest, opts capapi.EmitOptions) (*CompileResult, error) {
	if c == nil || c.registry == nil {
		return nil, fmt.Errorf("capability.Compile: registry is nil")
	}
	if req == nil || len(req.SourceInstincts) == 0 {
		return &CompileResult{}, nil
	}
	if len(req.TargetKinds) == 0 {
		req.TargetKinds = []schema.CapabilityArtifactKind{schema.ArtifactKindSkill}
	}
	if len(req.Targets) == 0 {
		req.Targets = []schema.Target{{Format: "agent-skill"}}
	}

	result := &CompileResult{}
	for _, inst := range req.SourceInstincts {
		for _, kind := range req.TargetKinds {
			artifact := synthesise(inst, kind, req.Scope)
			for _, target := range req.Targets {
				artifact.Target = target
				artifact.ComputeChecksum()
				result.Artifacts = append(result.Artifacts, artifact)
				if req.DryRun {
					continue
				}
				emitter, err := c.registry.Lookup(target.Format)
				if err != nil {
					return nil, fmt.Errorf("capability.Compile target=%s: %w", target.Format, err)
				}
				emission, err := emitter.Emit(ctx, artifact, opts)
				if err != nil {
					return nil, fmt.Errorf("capability.Compile emit target=%s artifact=%s: %w", target.Format, artifact.ID, err)
				}
				if emission != nil {
					result.Emissions = append(result.Emissions, *emission)
				}
			}
		}
	}
	return result, nil
}

// synthesise maps a single Instinct onto a CapabilityArtifact at
// the requested Kind. v0.6 the mapping is intentionally generic
// (rule body → description + invocation_condition); v0.6.x adds
// kind-specific synthesisers as separate functions registered in
// internal/capability/synthesise_*.go.
func synthesise(inst schema.Instinct, kind schema.CapabilityArtifactKind, scope schema.Scope) schema.CapabilityArtifact {
	return schema.CapabilityArtifact{
		Stability:           schema.StabilityExperimental,
		ID:                  inst.ID,
		Kind:                kind,
		Name:                instinctRuleSummary(inst.Rule),
		Description:         inst.Rule,
		InvocationCondition: inst.HowToApply,
		Confidence:          inst.Confidence,
		Version:             "v0.6.0",
		SourceInstincts:     []string{inst.ID},
		Scope:               scope,
		CreatedAt:           time.Now().UTC(),
		Invocation: schema.Invocation{
			Trigger: inst.HowToApply,
		},
		Contract: schema.Contract{
			Outputs: []string{"narrated rationale"},
		},
		Provenance: schema.Provenance{
			InstinctIDs:          []string{inst.ID},
			EvidenceFingerprints: inst.EvidenceRefs,
			GeneratedBy:          "bough@v0.6.0",
		},
	}
}

// instinctRuleSummary trims a rule body to a single-line Name. We
// keep this in the compiler rather than schema because the trimming
// rule is a UI concern; schema stays an honest data carrier.
func instinctRuleSummary(rule string) string {
	const maxLen = 80
	if len(rule) <= maxLen {
		return rule
	}
	// Use the rune-safe rather than byte-safe truncation so multi-
	// byte UTF-8 (Japanese rule bodies for instance) does not yield
	// invalid sequences.
	runes := []rune(rule)
	if len(runes) <= maxLen {
		return rule
	}
	return string(runes[:maxLen]) + "…"
}
