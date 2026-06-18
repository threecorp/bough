// Command bough-plugin-redis is the Hashicorp go-plugin gRPC server
// for the Redis 7 engine.
package main

import (
	api "github.com/ikeikeikeike/bough/plugins/engine/api"
	redisprovider "github.com/ikeikeikeike/bough/plugins/engine/redis"
	"github.com/hashicorp/go-plugin"
)

func main() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: api.Handshake,
		Plugins: map[string]plugin.Plugin{
			api.EngineProviderPluginKey: &api.EngineProviderPlugin{Impl: redisprovider.New()},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}
