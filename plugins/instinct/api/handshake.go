package api

import "github.com/hashicorp/go-plugin"

// Handshake is the v0.5.0 InstinctMinter magic-cookie negotiation.
// The cookie is distinct from BOUGH_ENGINE_PLUGIN / BOUGH_MEMORY_PLUGIN
// so a binary that registers more than one contract is unambiguous
// about which is being dispensed.
var Handshake = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "BOUGH_INSTINCT_PLUGIN",
	MagicCookieValue: "v1",
}

// InstinctMinterPluginKey is the registry key under which the gRPC
// plugin is exposed.
const InstinctMinterPluginKey = "instinct_minter"

// PluginMap registers InstinctMinterPlugin under
// InstinctMinterPluginKey. Both host and plugin pass this map so
// the wire format is symmetric.
var PluginMap = map[string]plugin.Plugin{
	InstinctMinterPluginKey: &InstinctMinterPlugin{},
}
