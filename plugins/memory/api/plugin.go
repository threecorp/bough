package api

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	pb "github.com/ikeikeikeike/bough/plugins/memory/api/proto"
)

// MemoryBackendPlugin glues a Go MemoryBackend implementation to
// go-plugin's gRPC transport. On the plugin side `Impl` is set to
// the concrete provider (e.g. the SQLite reference-fallback); on
// the host side `Impl` is left nil and GRPCClient produces a wire-
// backed client.
//
// Embedding plugin.Plugin{} keeps go-plugin's required interface
// methods satisfied without us having to spell them out — only the
// gRPC pair is interesting.
type MemoryBackendPlugin struct {
	plugin.Plugin
	Impl MemoryBackend
}

func (p *MemoryBackendPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	pb.RegisterMemoryBackendServer(s, &grpcServer{impl: p.Impl})
	return nil
}

func (p *MemoryBackendPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return &grpcClient{client: pb.NewMemoryBackendClient(c)}, nil
}
