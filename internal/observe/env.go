package observe

import (
	"sort"
	"strings"
)

// AnthropicAPIEnvVars are the environment variables Claude CLI
// (and the Anthropic SDKs) consult to switch from subscription auth
// to API-key billing. When bough spawns `claude --print` as a
// subprocess we strip every one of these from the child env so the
// child can never silently flip to API billing under the operator's
// nose. The operator's main `claude` interactive session keeps the
// original env via the parent shell — only the subprocess is
// affected.
var AnthropicAPIEnvVars = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_BEDROCK_BASE_URL",
	"ANTHROPIC_VERTEX_BASE_URL",
	"ANTHROPIC_VERTEX_PROJECT_ID",
	"CLAUDE_API_KEY",
	// CLAUDE_CODE_USE_BEDROCK / CLAUDE_CODE_USE_VERTEX are the actual
	// enable switches that route Claude Code onto Bedrock/Vertex
	// billing — the ANTHROPIC_BEDROCK_BASE_URL / ANTHROPIC_VERTEX_*
	// vars above are only auxiliary endpoint/project overrides that do
	// nothing unless one of these two is also set. Omitting them let
	// an operator's CLAUDE_CODE_USE_BEDROCK=1 (a normal enterprise,
	// AWS-billed Claude Code setup) pass straight through into the
	// spawned `claude --print` subprocess, silently billing the
	// operator's AWS account instead of their subscription.
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
}

// SanitizeAnthropicEnv returns a copy of env (KEY=VALUE strings as
// returned by os.Environ()) with every API-key-style variable
// removed. The result is intended to be passed into
// exec.Cmd.Env so the spawned `claude --print` subprocess falls
// back to the operator's subscription auth (= ~/.claude.json
// oauth_token) instead of API billing.
// SelfInvocationEnv marks a claude subprocess as one BOUGH ITSELF
// spawned (the observer mint, the gate judge, the CLAUDE.md proposal).
// The hooks installed in that subprocess's session see this variable and
// record nothing: without it, bough's own judge/mint prompts were
// captured as UserPromptSubmit observations, and the correction words
// those prompts legitimately contain ("a wrong \"true\" quarantines a
// useful instinct") read as the OPERATOR correcting the assistant —
// measured 2026-08-07: 106 of the 107 correction-flagged sessions owed
// their flag to bough's own prompt text, not to the operator.
const SelfInvocationEnv = "BOUGH_SELF_INVOCATION"

func SanitizeAnthropicEnv(env []string) []string {
	if len(env) == 0 {
		return env
	}
	drop := map[string]struct{}{}
	for _, k := range AnthropicAPIEnvVars {
		drop[strings.ToUpper(k)] = struct{}{}
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			out = append(out, kv)
			continue
		}
		key := strings.ToUpper(kv[:eq])
		if _, dropIt := drop[key]; dropIt {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// DetectAnthropicAPIVars scans env and returns the names of every
// API-key-style variable that is present. `bough doctor` calls this
// to warn the operator when API keys would otherwise silently
// override subscription auth in nested processes (= e.g. when a
// user runs bough from a shell that exports ANTHROPIC_API_KEY).
func DetectAnthropicAPIVars(env []string) []string {
	want := map[string]struct{}{}
	for _, k := range AnthropicAPIEnvVars {
		want[strings.ToUpper(k)] = struct{}{}
	}
	found := []string{}
	seen := map[string]struct{}{}
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		key := strings.ToUpper(kv[:eq])
		if _, ok := want[key]; !ok {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		found = append(found, key)
	}
	sort.Strings(found)
	return found
}
