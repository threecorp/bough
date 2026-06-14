// Package api carries the host-↔-plugin contract for `bough-plugin-<kind>`
// database engines. Host and plugin link this package; the generated
// gRPC stubs sit under api/proto.
package api

import "github.com/hashicorp/go-plugin"

// Handshake is the magic-cookie negotiation between the bough host and a
// db plugin. Bumping ProtocolVersion is the gate for backwards-
// incompatible db.proto changes — host and plugin must match.
var Handshake = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "BOUGH_DB_PLUGIN",
	MagicCookieValue: "v1",
}

// DBProviderPluginKey is the registry key under which the gRPC plugin
// is exposed; rpc.Dispense(DBProviderPluginKey) on the host side and
// plugin.Serve({Plugins: {DBProviderPluginKey: ...}}) on the plugin
// side must agree.
const DBProviderPluginKey = "db_provider"

// PluginMap registers DBProviderPlugin under DBProviderPluginKey. Both
// the host and the plugin pass this map to go-plugin so the wire format
// is symmetric.
var PluginMap = map[string]plugin.Plugin{
	DBProviderPluginKey: &DBProviderPlugin{},
}
