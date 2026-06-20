package api

import "github.com/hashicorp/go-plugin"

// Handshake is the v0.5.0 CapabilityCompiler magic-cookie
// negotiation. v0.5 ships this contract as STUB — the host does
// not actually discover capability plugins, so the cookie reservation
// here is to lock the protocol name down for v0.6 implementations.
var Handshake = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "BOUGH_CAPABILITY_PLUGIN",
	MagicCookieValue: "v1",
}

// CapabilityCompilerPluginKey is the registry key under which the
// gRPC plugin is exposed.
const CapabilityCompilerPluginKey = "capability_compiler"

// PluginMap registers CapabilityCompilerPlugin under
// CapabilityCompilerPluginKey.
var PluginMap = map[string]plugin.Plugin{
	CapabilityCompilerPluginKey: &CapabilityCompilerPlugin{},
}
