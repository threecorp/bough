package api

import (
	"context"
	"errors"

	pb "github.com/ikeikeikeike/bough/plugins/capability/api/proto"
)

// grpcClient routes Go args → pb wire and rematerialises
// response.error as a Go error.
type grpcClient struct {
	client pb.CapabilityCompilerClient
}

func (c *grpcClient) Compile(ctx context.Context, req *CompileReq) (*CompileResp, error) {
	kinds := make([]pb.CapabilityArtifactKind, len(req.TargetKinds))
	for i, k := range req.TargetKinds {
		kinds[i] = kindToProto(k)
	}
	pbReq := &pb.CompileRequest{
		SourceInstinctIds: req.SourceInstinctIDs,
		TargetKinds:       kinds,
		Scope:             scopeToProto(req.Scope),
		RequireApproval:   req.RequireApproval,
		DryRun:            req.DryRun,
		EvidencePolicy: &pb.EvidencePolicy{
			RequireMinTraces: int32(req.EvidencePolicy.RequireMinTraces),
			AllowedSources:   req.EvidencePolicy.AllowedSources,
		},
	}
	resp, err := c.client.Compile(ctx, pbReq)
	if err != nil {
		return nil, err
	}
	if e := resp.GetError(); e != "" {
		return nil, errors.New(e)
	}
	out := make([]*CapabilityArtifact, 0, len(resp.GetCandidates()))
	for _, pa := range resp.GetCandidates() {
		if a := artifactFromProto(pa); a != nil {
			out = append(out, a)
		}
	}
	return &CompileResp{Candidates: out}, nil
}
