// Command bough-plugin-elasticsearch is the Hashicorp go-plugin gRPC
// server for the Elasticsearch 7.x engine.
package main

import (
	api "github.com/ikeikeikeike/bough/plugins/engine/api"
	esprovider "github.com/ikeikeikeike/bough/plugins/engine/elasticsearch"
	"github.com/hashicorp/go-plugin"
)

func main() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: api.Handshake,
		Plugins: map[string]plugin.Plugin{
			api.EngineProviderPluginKey: &api.EngineProviderPlugin{Impl: esprovider.New()},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}
