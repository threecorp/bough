package api

import "github.com/hashicorp/go-plugin"

// Handshake is the v0.5.0 SkillEvaluator magic-cookie negotiation.
// v0.5 ships this contract as STUB — the host does not discover
// evaluator plugins, so the cookie reservation is to lock the
// protocol name for v0.7+ implementations.
var Handshake = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "BOUGH_EVALUATOR_PLUGIN",
	MagicCookieValue: "v1",
}

// SkillEvaluatorPluginKey is the registry key under which the gRPC
// plugin is exposed.
const SkillEvaluatorPluginKey = "skill_evaluator"

// PluginMap registers SkillEvaluatorPlugin under
// SkillEvaluatorPluginKey.
var PluginMap = map[string]plugin.Plugin{
	SkillEvaluatorPluginKey: &SkillEvaluatorPlugin{},
}
