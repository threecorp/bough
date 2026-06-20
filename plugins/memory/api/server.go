package api

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/ikeikeikeike/bough/plugins/memory/api/proto"
)

// grpcServer is the plugin-side adapter: it implements the generated
// pb.MemoryBackendServer interface by delegating to a Go
// MemoryBackend Impl. Errors are surfaced into the wire response's
// `error` string so non-Go plugins can produce them without
// learning gRPC status codes.
type grpcServer struct {
	pb.UnimplementedMemoryBackendServer
	impl MemoryBackend
}

func (s *grpcServer) Health(ctx context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	resp, err := s.impl.Health(ctx, &HealthReq{})
	if err != nil {
		return &pb.HealthResponse{Error: err.Error()}, nil
	}
	return &pb.HealthResponse{
		BackendKind:   resp.BackendKind,
		PluginVersion: resp.PluginVersion,
	}, nil
}

func (s *grpcServer) Capabilities(ctx context.Context, _ *emptypb.Empty) (*pb.CapabilitiesResponse, error) {
	resp, err := s.impl.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	return &pb.CapabilitiesResponse{
		SemanticQuery:    resp.SemanticQuery,
		GraphQuery:       resp.GraphQuery,
		BulkExport:       resp.BulkExport,
		VectorSearch:     resp.VectorSearch,
		SupportsMetadata: resp.SupportsMetadata,
		PluginVersion:    resp.PluginVersion,
	}, nil
}

func (s *grpcServer) Store(ctx context.Context, r *pb.StoreRequest) (*pb.StoreResponse, error) {
	resp, err := s.impl.Store(ctx, &StoreReq{
		Instinct:        instinctFromProto(r.GetInstinct()),
		DedupeKey:       r.GetDedupeKey(),
		SourceEventID:   r.GetSourceEventId(),
		UpsertSemantics: r.GetUpsertSemantics(),
	})
	if err != nil {
		return &pb.StoreResponse{Error: err.Error()}, nil
	}
	return &pb.StoreResponse{
		StoredId:  resp.StoredID,
		WasUpsert: resp.WasUpsert,
	}, nil
}

func (s *grpcServer) Query(ctx context.Context, r *pb.QueryRequest) (*pb.QueryResponse, error) {
	resp, err := s.impl.Query(ctx, &QueryReq{
		Term:          r.GetTerm(),
		Scope:         scopeFromProto(r.GetScope()),
		MaxResults:    int(r.GetMaxResults()),
		MaxTokens:     int(r.GetMaxTokens()),
		MinConfidence: r.GetMinConfidence(),
	})
	if err != nil {
		return &pb.QueryResponse{Error: err.Error()}, nil
	}
	out := make([]*pb.QueryResult, len(resp.Results))
	for i, res := range resp.Results {
		out[i] = &pb.QueryResult{
			Instinct:        instinctToProto(res.Instinct),
			Score:           res.Score,
			EstimatedTokens: int32(res.EstimatedTokens),
			Truncated:       res.Truncated,
		}
	}
	return &pb.QueryResponse{Results: out}, nil
}

func (s *grpcServer) Forget(ctx context.Context, r *pb.ForgetRequest) (*pb.ForgetResponse, error) {
	_, err := s.impl.Forget(ctx, &ForgetReq{
		ID:     r.GetId(),
		Scope:  scopeFromProto(r.GetScope()),
		Reason: r.GetReason(),
	})
	if err != nil {
		return &pb.ForgetResponse{Error: err.Error()}, nil
	}
	return &pb.ForgetResponse{}, nil
}

func (s *grpcServer) Export(ctx context.Context, r *pb.ExportRequest) (*pb.ExportResponse, error) {
	resp, err := s.impl.Export(ctx, &ExportReq{
		Format:      r.GetFormat(),
		Scope:       scopeFromProto(r.GetScope()),
		StateFilter: r.GetStateFilter(),
	})
	if err != nil {
		return &pb.ExportResponse{Error: err.Error()}, nil
	}
	return &pb.ExportResponse{
		Payload:     resp.Payload,
		ContentType: resp.ContentType,
	}, nil
}

func (s *grpcServer) Import(ctx context.Context, r *pb.ImportRequest) (*pb.ImportResponse, error) {
	resp, err := s.impl.Import(ctx, &ImportReq{
		Format:            r.GetFormat(),
		Payload:           r.GetPayload(),
		OverwriteExisting: r.GetOverwriteExisting(),
	})
	if err != nil {
		return &pb.ImportResponse{Error: err.Error()}, nil
	}
	return &pb.ImportResponse{
		ImportedCount: int32(resp.ImportedCount),
		UpsertedCount: int32(resp.UpsertedCount),
		SkippedCount:  int32(resp.SkippedCount),
	}, nil
}

// scopeFromProto converts wire scope → Go scope. The wire shape
// uses plain strings (cheap for non-Go plugins); the Go shape uses
// the same strings but consumers may compare against schema.
// ScopeLevel constants.
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

// instinctFromProto / instinctToProto translate the wire <-> Go
// Instinct representation. Wire-side time fields are unix epoch
// seconds for cheap polyglot serialisation; Go-side fields are
// time.Time so coordinator code does not have to deal with int64s.
func instinctFromProto(p *pb.Instinct) Instinct {
	if p == nil {
		return Instinct{}
	}
	return Instinct{
		ID:           p.GetId(),
		Rule:         p.GetRule(),
		Why:          p.GetWhy(),
		HowToApply:   p.GetHowToApply(),
		Domain:       p.GetDomain(),
		Scope:        scopeFromProto(p.GetScope()),
		Source:       p.GetSource(),
		Confidence:   p.GetConfidence(),
		State:        p.GetState(),
		Hits:         int(p.GetHits()),
		LastHitAt:    fromUnix(p.GetLastHitAt()),
		CreatedAt:    fromUnix(p.GetCreatedAt()),
		SourceTraces: p.GetSourceTraces(),
		EvidenceRefs: p.GetEvidenceRefs(),
		DedupeKey:    p.GetDedupeKey(),
		MetadataJSON: p.GetMetadataJson(),
	}
}

func instinctToProto(i Instinct) *pb.Instinct {
	return &pb.Instinct{
		Id:           i.ID,
		Rule:         i.Rule,
		Why:          i.Why,
		HowToApply:   i.HowToApply,
		Domain:       i.Domain,
		Scope:        scopeToProto(i.Scope),
		Source:       i.Source,
		Confidence:   i.Confidence,
		State:        i.State,
		Hits:         int32(i.Hits),
		LastHitAt:    toUnix(i.LastHitAt),
		CreatedAt:    toUnix(i.CreatedAt),
		SourceTraces: i.SourceTraces,
		EvidenceRefs: i.EvidenceRefs,
		DedupeKey:    i.DedupeKey,
		MetadataJson: i.MetadataJSON,
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
