package api

import "github.com/hashicorp/go-plugin"

// Handshake is the v0.5.0 MemoryBackend magic-cookie negotiation
// between the bough host and a memory plugin. Bumping
// ProtocolVersion is the gate for backwards-incompatible
// memory.proto changes — host and plugin must match.
//
// The magic cookie is distinct from BOUGH_ENGINE_PLUGIN so a single
// `bough-plugin-X` binary that registers both (rare; the conformance
// mock does this) is unambiguous about which contract is being
// dispensed.
var Handshake = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "BOUGH_MEMORY_PLUGIN",
	MagicCookieValue: "v1",
}

// MemoryBackendPluginKey is the registry key under which the gRPC
// plugin is exposed; rpc.Dispense(MemoryBackendPluginKey) on the
// host side and plugin.Serve({Plugins: {MemoryBackendPluginKey:
// ...}}) on the plugin side must agree.
const MemoryBackendPluginKey = "memory_backend"

// PluginMap registers MemoryBackendPlugin under
// MemoryBackendPluginKey. Both the host and the plugin pass this
// map to go-plugin so the wire format is symmetric.
var PluginMap = map[string]plugin.Plugin{
	MemoryBackendPluginKey: &MemoryBackendPlugin{},
}
