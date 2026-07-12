package api

import "github.com/hashicorp/go-plugin"

// Handshake is the v0.4.0 EngineProvider magic-cookie negotiation
// between the bough host and an engine plugin. Bumping ProtocolVersion
// is the gate for engine.proto changes — host and plugin must match.
// Per CONTRACT.md, every field added after v0.4.0 rides along with a
// bump even when it is technically proto3-additive: bough co-ships the
// host and all bough-plugin-* binaries from one release and installs
// them together, so a bump costs nothing in practice while turning an
// accidental partial upgrade (a stale plugin binary left on PATH) into
// a loud handshake error instead of a silently-dropped field.
//
//	v2 (ProtocolVersion 2) — v0.4.0 EngineProvider baseline.
//	v3 (ProtocolVersion 3) — UpRequest.plugins (PluginSpec) added.
//
// MagicCookieValue is the plugin-TYPE identity (a friendly "this is a
// bough engine plugin" check), not the compatibility gate, so it stays
// constant across ProtocolVersion bumps.
var Handshake = plugin.HandshakeConfig{
	ProtocolVersion:  3,
	MagicCookieKey:   "BOUGH_ENGINE_PLUGIN",
	MagicCookieValue: "v2",
}

// EngineProviderPluginKey is the registry key under which the gRPC
// plugin is exposed; rpc.Dispense(EngineProviderPluginKey) on the
// host side and plugin.Serve({Plugins: {EngineProviderPluginKey:
// ...}}) on the plugin side must agree.
const EngineProviderPluginKey = "engine_provider"

// PluginMap registers EngineProviderPlugin under
// EngineProviderPluginKey. Both the host and the plugin pass this map
// to go-plugin so the wire format is symmetric.
var PluginMap = map[string]plugin.Plugin{
	EngineProviderPluginKey: &EngineProviderPlugin{},
}
