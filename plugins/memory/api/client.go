package api

import (
	"context"
	"errors"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/ikeikeikeike/bough/plugins/memory/api/proto"
)

// grpcClient is the host-side adapter: it implements the Go
// MemoryBackend interface by routing through a generated
// pb.MemoryBackendClient gRPC stub. Errors come back through the
// wire response's `error` string and are rematerialised here as
// plain Go errors so callers can `errors.Is` / wrap as usual.
type grpcClient struct {
	client pb.MemoryBackendClient
}

func (c *grpcClient) Health(ctx context.Context, _ *HealthReq) (*HealthResp, error) {
	resp, err := c.client.Health(ctx, &pb.HealthRequest{})
	if err != nil {
		return nil, err
	}
	if e := resp.GetError(); e != "" {
		return nil, errors.New(e)
	}
	return &HealthResp{
		BackendKind:   resp.GetBackendKind(),
		PluginVersion: resp.GetPluginVersion(),
	}, nil
}

func (c *grpcClient) Capabilities(ctx context.Context) (*CapabilitiesResp, error) {
	resp, err := c.client.Capabilities(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, err
	}
	return &CapabilitiesResp{
		// v0.5
		SemanticQuery:    resp.GetSemanticQuery(),
		GraphQuery:       resp.GetGraphQuery(),
		BulkExport:       resp.GetBulkExport(),
		VectorSearch:     resp.GetVectorSearch(),
		SupportsMetadata: resp.GetSupportsMetadata(),
		PluginVersion:    resp.GetPluginVersion(),
		// v0.6 (= round 4 priority A12)
		TemporalQuery:       resp.GetTemporalQuery(),
		MetadataFilter:      resp.GetMetadataFilter(),
		NamespaceIsolation:  resp.GetNamespaceIsolation(),
		SoftDelete:          resp.GetSoftDelete(),
		BulkImport:          resp.GetBulkImport(),
		DedupeKey:           resp.GetDedupeKey(),
		SourceEventID:       resp.GetSourceEventId(),
		TTL:                 resp.GetTtl(),
		EventualConsistency: resp.GetEventualConsistency(),
		MaxBatchSize:        int(resp.GetMaxBatchSize()),
		MaxQueryTokens:      int(resp.GetMaxQueryTokens()),
	}, nil
}

func (c *grpcClient) Store(ctx context.Context, req *StoreReq) (*StoreResp, error) {
	resp, err := c.client.Store(ctx, &pb.StoreRequest{
		Instinct:        instinctToProto(req.Instinct),
		DedupeKey:       req.DedupeKey,
		SourceEventId:   req.SourceEventID,
		UpsertSemantics: req.UpsertSemantics,
	})
	if err != nil {
		return nil, err
	}
	if e := resp.GetError(); e != "" {
		return nil, errors.New(e)
	}
	return &StoreResp{
		StoredID:  resp.GetStoredId(),
		WasUpsert: resp.GetWasUpsert(),
	}, nil
}

func (c *grpcClient) Query(ctx context.Context, req *QueryReq) (*QueryResp, error) {
	resp, err := c.client.Query(ctx, &pb.QueryRequest{
		Term:          req.Term,
		Scope:         scopeToProto(req.Scope),
		MaxResults:    int32(req.MaxResults),
		MaxTokens:     int32(req.MaxTokens),
		MinConfidence: req.MinConfidence,
	})
	if err != nil {
		return nil, err
	}
	if e := resp.GetError(); e != "" {
		return nil, errors.New(e)
	}
	out := make([]QueryResult, len(resp.GetResults()))
	for i, res := range resp.GetResults() {
		out[i] = QueryResult{
			Instinct:        instinctFromProto(res.GetInstinct()),
			Score:           res.GetScore(),
			EstimatedTokens: int(res.GetEstimatedTokens()),
			Truncated:       res.GetTruncated(),
		}
	}
	return &QueryResp{Results: out}, nil
}

func (c *grpcClient) Forget(ctx context.Context, req *ForgetReq) (*ForgetResp, error) {
	resp, err := c.client.Forget(ctx, &pb.ForgetRequest{
		Id:     req.ID,
		Scope:  scopeToProto(req.Scope),
		Reason: req.Reason,
	})
	if err != nil {
		return nil, err
	}
	if e := resp.GetError(); e != "" {
		return nil, errors.New(e)
	}
	return &ForgetResp{}, nil
}

func (c *grpcClient) Export(ctx context.Context, req *ExportReq) (*ExportResp, error) {
	resp, err := c.client.Export(ctx, &pb.ExportRequest{
		Format:      req.Format,
		Scope:       scopeToProto(req.Scope),
		StateFilter: req.StateFilter,
	})
	if err != nil {
		return nil, err
	}
	if e := resp.GetError(); e != "" {
		return nil, errors.New(e)
	}
	return &ExportResp{
		Payload:     resp.GetPayload(),
		ContentType: resp.GetContentType(),
	}, nil
}

func (c *grpcClient) Import(ctx context.Context, req *ImportReq) (*ImportResp, error) {
	resp, err := c.client.Import(ctx, &pb.ImportRequest{
		Format:            req.Format,
		Payload:           req.Payload,
		OverwriteExisting: req.OverwriteExisting,
	})
	if err != nil {
		return nil, err
	}
	if e := resp.GetError(); e != "" {
		return nil, errors.New(e)
	}
	return &ImportResp{
		ImportedCount: int(resp.GetImportedCount()),
		UpsertedCount: int(resp.GetUpsertedCount()),
		SkippedCount:  int(resp.GetSkippedCount()),
	}, nil
}
