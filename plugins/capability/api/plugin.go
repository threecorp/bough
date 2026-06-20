package api

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	pb "github.com/ikeikeikeike/bough/plugins/capability/api/proto"
)

// CapabilityCompilerPlugin glues a Go CapabilityCompiler impl to
// go-plugin gRPC. v0.5 ships only the type — no host code spawns
// a capability plugin yet. v0.6 hooks `bough capability compile`
// through this plugin map.
type CapabilityCompilerPlugin struct {
	plugin.Plugin
	Impl CapabilityCompiler
}

func (p *CapabilityCompilerPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	pb.RegisterCapabilityCompilerServer(s, &grpcServer{impl: p.Impl})
	return nil
}

func (p *CapabilityCompilerPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return &grpcClient{client: pb.NewCapabilityCompilerClient(c)}, nil
}
