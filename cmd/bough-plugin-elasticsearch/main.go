// Command bough-plugin-elasticsearch is the Hashicorp go-plugin gRPC
// server for the Elasticsearch 7.x database engine.
package main

import (
	api "github.com/ikeikeikeike/bough/plugins/db/api"
	esprovider "github.com/ikeikeikeike/bough/plugins/db/elasticsearch"
	"github.com/hashicorp/go-plugin"
)

func main() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: api.Handshake,
		Plugins: map[string]plugin.Plugin{
			api.DBProviderPluginKey: &api.DBProviderPlugin{Impl: esprovider.New()},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}
