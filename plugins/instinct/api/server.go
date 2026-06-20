package api

import (
	"context"
	"time"

	pb "github.com/ikeikeikeike/bough/plugins/instinct/api/proto"
)

// grpcServer translates pb wire messages → Go types → InstinctMinter.Impl
// and bridges errors via the response.error string for polyglot
// plugin authors.
type grpcServer struct {
	pb.UnimplementedInstinctMinterServer
	impl InstinctMinter
}

func (s *grpcServer) Mint(ctx context.Context, r *pb.MintRequest) (*pb.MintResponse, error) {
	bundles := make([]TraceBundle, len(r.GetTraceBundles()))
	for i, b := range r.GetTraceBundles() {
		bundles[i] = TraceBundle{
			ID:            b.GetId(),
			Source:        b.GetSource(),
			Scope:         scopeFromProto(b.GetScope()),
			CapturedAt:    fromUnix(b.GetCapturedAt()),
			Content:       b.GetContent(),
			EvidenceRef:   b.GetEvidenceRef(),
			SourceEventID: b.GetSourceEventId(),
		}
	}
	resp, err := s.impl.Mint(ctx, &MintReq{
		TraceBundles: bundles,
		Scope:        scopeFromProto(r.GetScope()),
	})
	if err != nil {
		return &pb.MintResponse{Error: err.Error()}, nil
	}
	out := make([]*pb.InstinctCandidate, len(resp.Candidates))
	for i, c := range resp.Candidates {
		if c == nil {
			continue
		}
		out[i] = candidateToProto(c)
	}
	return &pb.MintResponse{Candidates: out}, nil
}

func scopeFromProto(p *pb.Scope) Scope {
	if p == nil {
		return Scope{}
	}
	return Scope{
		Level:      p.GetLevel(),
		WorktreeID: p.GetWorktreeId(),
		RepoName:   p.GetRepoName(),
	}
}

func scopeToProto(s Scope) *pb.Scope {
	return &pb.Scope{
		Level:      s.Level,
		WorktreeId: s.WorktreeID,
		RepoName:   s.RepoName,
	}
}

func candidateFromProto(p *pb.InstinctCandidate) *InstinctCandidate {
	if p == nil {
		return nil
	}
	return &InstinctCandidate{
		ID:           p.GetId(),
		Rule:         p.GetRule(),
		Why:          p.GetWhy(),
		HowToApply:   p.GetHowToApply(),
		Domain:       p.GetDomain(),
		Scope:        scopeFromProto(p.GetScope()),
		Source:       p.GetSource(),
		Confidence:   p.GetConfidence(),
		State:        p.GetState(),
		SourceTraces: p.GetSourceTraces(),
		CreatedAt:    fromUnix(p.GetCreatedAt()),
		DedupeKey:    p.GetDedupeKey(),
	}
}

func candidateToProto(c *InstinctCandidate) *pb.InstinctCandidate {
	return &pb.InstinctCandidate{
		Id:           c.ID,
		Rule:         c.Rule,
		Why:          c.Why,
		HowToApply:   c.HowToApply,
		Domain:       c.Domain,
		Scope:        scopeToProto(c.Scope),
		Source:       c.Source,
		Confidence:   c.Confidence,
		State:        c.State,
		SourceTraces: c.SourceTraces,
		CreatedAt:    toUnix(c.CreatedAt),
		DedupeKey:    c.DedupeKey,
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
