package instinct

import "github.com/ikeikeikeike/bough/pkg/schema"

// ConfidencePolicy enforces the source-aware confidence ceiling
// (round 1 AI #4 / round 2 AI #1). A minter that has no idea about
// the value of its own output may stamp a candidate with
// Confidence: 0.95 even when the source is just an LLM-only
// hallucination; ClampInitial silently lowers that to the source's
// configured ceiling.
//
// Ceilings come from `.bough.yaml`'s
// `instinct.confidence.sources` map. The applyInstinctDefaults
// helper in internal/config seeds the four canonical sources
// (explicit_user_feedback / test_failure / session_summary /
// llm_only); operators can extend the map.
type ConfidencePolicy struct {
	ceilings       map[string]float64
	reinforceDelta float64
}

// NewConfidencePolicy reads the source-ceiling map and the
// reinforce delta straight off the canonical config struct. The
// host calls this once per coordinator startup; the policy is
// then stateless and safe to share across goroutines.
func NewConfidencePolicy(sources map[string]float64, reinforceDelta float64) *ConfidencePolicy {
	return &ConfidencePolicy{
		ceilings:       sources,
		reinforceDelta: reinforceDelta,
	}
}

// ClampInitial lowers proposed if the candidate's Source has a
// ceiling and proposed exceeds it. Sources unknown to the policy
// pass through unchanged so a v0.6 source name added after the
// policy was loaded does not silently get a 0.0 ceiling.
func (p *ConfidencePolicy) ClampInitial(c *schema.InstinctCandidate, proposed float64) {
	if p == nil || c == nil {
		return
	}
	ceiling, ok := p.ceilings[string(c.Source)]
	if !ok || proposed <= ceiling {
		c.Confidence = proposed
		return
	}
	c.Confidence = ceiling
}

// Reinforce bumps the confidence of an existing Instinct by
// reinforce_delta on a dedupe-key match (= the row was Store'd
// again). The ceiling derived from the original source still
// caps the result so a row with Source: llm_only never floats past
// 0.3 no matter how often the LLM re-emits the same idea.
func (p *ConfidencePolicy) Reinforce(i *schema.Instinct) {
	if p == nil || i == nil {
		return
	}
	bumped := i.Confidence + p.reinforceDelta
	ceiling, ok := p.ceilings[string(i.Source)]
	if ok && bumped > ceiling {
		bumped = ceiling
	}
	if bumped > 1.0 {
		bumped = 1.0
	}
	i.Confidence = bumped
	i.Hits++
}
