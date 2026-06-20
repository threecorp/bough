package api

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	pb "github.com/ikeikeikeike/bough/plugins/instinct/api/proto"
)

// InstinctMinterPlugin glues a Go InstinctMinter impl to go-plugin
// gRPC. Plugin-side sets Impl; host-side leaves Impl nil and
// GRPCClient produces a wire-backed client.
type InstinctMinterPlugin struct {
	plugin.Plugin
	Impl InstinctMinter
}

func (p *InstinctMinterPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	pb.RegisterInstinctMinterServer(s, &grpcServer{impl: p.Impl})
	return nil
}

func (p *InstinctMinterPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return &grpcClient{client: pb.NewInstinctMinterClient(c)}, nil
}
