package schema

// EvidencePolicy gates how aggressive a CapabilityCompiler (v0.6+)
// or a SkillEvaluator (v0.7+) is allowed to be when promoting
// candidate artifacts. The host's coordinator passes EvidencePolicy
// into every Compile / Evaluate RPC so plugin authors do not need
// to re-derive sensible thresholds.
//
// RequireMinTraces=3 keeps single-shot anecdotes from being
// promoted: a one-off observation is not enough evidence for a
// reusable artifact, even if the LLM-backed minter rates it highly.
//
// AllowedSources whitelists the TraceSource values that count
// toward the minimum. Production-tight profiles will whitelist
// only "explicit_user_feedback" and "test_failure"; experimental
// profiles may allow "session_summary" and "llm_only" too.
type EvidencePolicy struct {
	RequireMinTraces int
	AllowedSources   []TraceSource
}
