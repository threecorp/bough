// Package evolve ports the threecorp ECC `/evolve-skill-manual-v3`
// clustering pipeline into Go. The pipeline turns the accumulated
// per-project instinct corpus into evolved skills / agents /
// commands via a 5-gate decision flow:
//
//	cluster discovery → GATE 1 member count → GATE 2 cohesion →
//	GATE 3' lexicon coverage → GATE 4 relative isolation →
//	GATE 5 LLM semantic judge
//
// GATE 1-4 are mechanical (= pure functions over token sets); GATE 5
// is the only non-mechanical gate and runs through the Claude CLI
// subprocess (internal/provider/claudecli) so the LLM cost stays
// inside the operator's subscription.
//
// The thresholds are ECC v3 verbatim:
//
//	MEMBER_MIN           = 2
//	COH_MIN              = 0.20
//	LEXICON_COVERAGE_MAX = 0.55
//	REL_ISOLATION_MIN    = 0.40
//
// They are exported as the default Thresholds value; the CLI lets an
// operator override them via .bough.yaml and every generated
// artifact records the thresholds it was minted under.
package evolve

import "strings"

// stopwords are dropped from token sets before any jaccard maths.
// The list mirrors ECC's tokenize spec: English function words plus
// the high-frequency tooling verbs that co-occur across nearly every
// instinct (= "use", "run", "file") and would otherwise inflate
// cohesion between topically-unrelated instincts.
var stopwords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "and": {}, "or": {}, "but": {},
	"if": {}, "then": {}, "else": {}, "when": {}, "while": {},
	"for": {}, "to": {}, "of": {}, "in": {}, "on": {}, "at": {},
	"by": {}, "with": {}, "from": {}, "into": {}, "as": {}, "is": {},
	"are": {}, "be": {}, "was": {}, "were": {}, "this": {}, "that": {},
	"it": {}, "its": {}, "do": {}, "does": {}, "done": {}, "not": {},
	"no": {}, "yes": {}, "can": {}, "will": {}, "should": {},
	"must": {}, "may": {}, "always": {}, "never": {},
	// high-frequency tooling verbs (= ECC noise list)
	"use": {}, "using": {}, "used": {}, "run": {}, "running": {},
	"file": {}, "files": {}, "make": {}, "via": {}, "after": {},
	"before": {},
}

// Tokenize lower-cases s, splits on non-alphanumeric boundaries, and
// drops stopwords + single-character tokens. The result is the
// deduplicated token set used for every jaccard computation.
//
// ECC tokenizes (id + trigger + action); callers concatenate those
// fields before calling Tokenize so the same surface area feeds the
// clustering as the upstream pipeline.
func Tokenize(s string) map[string]struct{} {
	out := map[string]struct{}{}
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		w := cur.String()
		cur.Reset()
		if len(w) < 2 {
			return
		}
		if _, stop := stopwords[w]; stop {
			return
		}
		out[w] = struct{}{}
	}
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// Jaccard returns |a ∩ b| / |a ∪ b| over two token sets. Empty-vs-
// empty returns 0 (= ECC convention: two contentless instincts are
// not "similar", they're both noise).
func Jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inter := 0
	// iterate the smaller set for the intersection scan
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	for k := range small {
		if _, ok := large[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// union merges every token set in sets into one. Used to build the
// candidate-token union (GATE 3') and the prior-all-tokens union.
func union(sets ...map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for _, s := range sets {
		for k := range s {
			out[k] = struct{}{}
		}
	}
	return out
}

// coverage returns |candidate ∩ priorAll| / |candidate| — the GATE 3'
// lexicon-coverage ratio. Returns 0 for an empty candidate so a
// contentless candidate never trips the "already inside prior
// vocabulary" rejection (it fails GATE 1/2 first anyway).
func coverage(candidate, priorAll map[string]struct{}) float64 {
	if len(candidate) == 0 {
		return 0
	}
	inter := 0
	for k := range candidate {
		if _, ok := priorAll[k]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(candidate))
}
