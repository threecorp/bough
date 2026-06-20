package api

import (
	"context"
	"time"

	pb "github.com/ikeikeikeike/bough/plugins/capability/api/proto"
)

// grpcServer translates pb wire messages → Go types → CapabilityCompiler.Impl.
// v0.5 plugin authors who want to prototype against this contract
// can build against this adapter today; the host will start
// discovering capability plugins in v0.6.
type grpcServer struct {
	pb.UnimplementedCapabilityCompilerServer
	impl CapabilityCompiler
}

func (s *grpcServer) Compile(ctx context.Context, r *pb.CompileRequest) (*pb.CompileResponse, error) {
	kinds := make([]CapabilityArtifactKind, len(r.GetTargetKinds()))
	for i, k := range r.GetTargetKinds() {
		kinds[i] = kindFromProto(k)
	}
	policy := EvidencePolicy{}
	if p := r.GetEvidencePolicy(); p != nil {
		policy.RequireMinTraces = int(p.GetRequireMinTraces())
		policy.AllowedSources = p.GetAllowedSources()
	}
	resp, err := s.impl.Compile(ctx, &CompileReq{
		SourceInstinctIDs: r.GetSourceInstinctIds(),
		TargetKinds:       kinds,
		Scope:             scopeFromProto(r.GetScope()),
		EvidencePolicy:    policy,
		RequireApproval:   r.GetRequireApproval(),
		DryRun:            r.GetDryRun(),
	})
	if err != nil {
		return &pb.CompileResponse{Error: err.Error()}, nil
	}
	out := make([]*pb.CapabilityArtifact, 0, len(resp.Candidates))
	for _, a := range resp.Candidates {
		if a == nil {
			continue
		}
		out = append(out, artifactToProto(a))
	}
	return &pb.CompileResponse{Candidates: out}, nil
}

func scopeFromProto(p *pb.Scope) Scope {
	if p == nil {
		return Scope{}
	}
	return Scope{Level: p.GetLevel(), WorktreeID: p.GetWorktreeId(), RepoName: p.GetRepoName()}
}

func scopeToProto(s Scope) *pb.Scope {
	return &pb.Scope{Level: s.Level, WorktreeId: s.WorktreeID, RepoName: s.RepoName}
}

var kindMap = map[CapabilityArtifactKind]pb.CapabilityArtifactKind{
	ArtifactMemory:    pb.CapabilityArtifactKind_ARTIFACT_KIND_MEMORY,
	ArtifactRule:      pb.CapabilityArtifactKind_ARTIFACT_KIND_RULE,
	ArtifactSkill:     pb.CapabilityArtifactKind_ARTIFACT_KIND_SKILL,
	ArtifactCommand:   pb.CapabilityArtifactKind_ARTIFACT_KIND_COMMAND,
	ArtifactWorkflow:  pb.CapabilityArtifactKind_ARTIFACT_KIND_WORKFLOW,
	ArtifactTool:      pb.CapabilityArtifactKind_ARTIFACT_KIND_TOOL,
	ArtifactAgent:     pb.CapabilityArtifactKind_ARTIFACT_KIND_AGENT,
	ArtifactEvaluator: pb.CapabilityArtifactKind_ARTIFACT_KIND_EVALUATOR,
}

func kindToProto(k CapabilityArtifactKind) pb.CapabilityArtifactKind {
	if p, ok := kindMap[k]; ok {
		return p
	}
	return pb.CapabilityArtifactKind_ARTIFACT_KIND_UNSPECIFIED
}

func kindFromProto(p pb.CapabilityArtifactKind) CapabilityArtifactKind {
	for k, v := range kindMap {
		if v == p {
			return k
		}
	}
	return ""
}

var stabilityMap = map[Stability]pb.Stability{
	StabilityStable:       pb.Stability_STABILITY_STABLE,
	StabilityPreview:      pb.Stability_STABILITY_PREVIEW,
	StabilityExperimental: pb.Stability_STABILITY_EXPERIMENTAL,
}

func stabilityToProto(s Stability) pb.Stability {
	if p, ok := stabilityMap[s]; ok {
		return p
	}
	return pb.Stability_STABILITY_UNSPECIFIED
}

func stabilityFromProto(p pb.Stability) Stability {
	for k, v := range stabilityMap {
		if v == p {
			return k
		}
	}
	return ""
}

func artifactFromProto(p *pb.CapabilityArtifact) *CapabilityArtifact {
	if p == nil {
		return nil
	}
	return &CapabilityArtifact{
		Stability:           stabilityFromProto(p.GetStability()),
		ID:                  p.GetId(),
		Kind:                kindFromProto(p.GetKind()),
		Name:                p.GetName(),
		Description:         p.GetDescription(),
		InvocationCondition: p.GetInvocationCondition(),
		Inputs:              p.GetInputs(),
		Outputs:             p.GetOutputs(),
		Steps:               p.GetSteps(),
		Constraints:         p.GetConstraints(),
		EvidenceRefs:        p.GetEvidenceRefs(),
		Confidence:          p.GetConfidence(),
		Version:             p.GetVersion(),
		SourceInstincts:     p.GetSourceInstincts(),
		Scope:               scopeFromProto(p.GetScope()),
		CreatedAt:           fromUnix(p.GetCreatedAt()),
		Payload:             p.GetPayload(),
	}
}

func artifactToProto(a *CapabilityArtifact) *pb.CapabilityArtifact {
	return &pb.CapabilityArtifact{
		Stability:           stabilityToProto(a.Stability),
		Id:                  a.ID,
		Kind:                kindToProto(a.Kind),
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
		Scope:               scopeToProto(a.Scope),
		CreatedAt:           toUnix(a.CreatedAt),
		Payload:             a.Payload,
	}
}

func fromUnix(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

func toUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
