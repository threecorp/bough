package api

import (
	"context"
	"errors"

	pb "github.com/ikeikeikeike/bough/plugins/instinct/api/proto"
)

// grpcClient translates Go args → pb wire and rematerialises
// response.error as a Go error so callers can errors.Is / wrap.
type grpcClient struct {
	client pb.InstinctMinterClient
}

func (c *grpcClient) Mint(ctx context.Context, req *MintReq) (*MintResp, error) {
	bundles := make([]*pb.TraceBundle, len(req.TraceBundles))
	for i, b := range req.TraceBundles {
		bundles[i] = &pb.TraceBundle{
			Id:            b.ID,
			Source:        b.Source,
			Scope:         scopeToProto(b.Scope),
			CapturedAt:    toUnix(b.CapturedAt),
			Content:       b.Content,
			EvidenceRef:   b.EvidenceRef,
			SourceEventId: b.SourceEventID,
		}
	}
	resp, err := c.client.Mint(ctx, &pb.MintRequest{
		TraceBundles: bundles,
		Scope:        scopeToProto(req.Scope),
	})
	if err != nil {
		return nil, err
	}
	if e := resp.GetError(); e != "" {
		return nil, errors.New(e)
	}
	out := make([]*InstinctCandidate, 0, len(resp.GetCandidates()))
	for _, pc := range resp.GetCandidates() {
		if c := candidateFromProto(pc); c != nil {
			out = append(out, c)
		}
	}
	return &MintResp{Candidates: out}, nil
}
