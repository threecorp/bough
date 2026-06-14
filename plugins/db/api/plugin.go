package api

import (
	"context"

	pb "github.com/ikeikeikeike/bough/plugins/db/api/proto"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// DBProviderPlugin glues a Go DBProvider implementation to go-plugin's
// gRPC transport. On the plugin side `Impl` is set to the concrete
// provider; on the host side `Impl` is left nil and GRPCClient produces
// a wire-backed client.
//
// Embedding plugin.Plugin{} keeps go-plugin's required interface methods
// satisfied without us having to spell them out — only the gRPC pair
// is interesting.
type DBProviderPlugin struct {
	plugin.Plugin
	Impl DBProvider
}

// GRPCServer is invoked by go-plugin on the plugin side at startup; it
// wires the user's Impl into the generated gRPC service stub.
func (p *DBProviderPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	pb.RegisterDBProviderServer(s, &grpcServer{impl: p.Impl})
	return nil
}

// GRPCClient is invoked by go-plugin on the host side after the plugin
// is up and listening; the returned value is what rpc.Dispense hands
// back to the host caller (typed as DBProvider through PluginMap).
func (p *DBProviderPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return &grpcClient{client: pb.NewDBProviderClient(c)}, nil
}
