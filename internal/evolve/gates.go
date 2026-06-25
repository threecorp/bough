package evolve

// Thresholds pins the four mechanical-gate cutoffs. The zero value
// is NOT valid; callers use DefaultThresholds (= ECC v3 verbatim)
// or load operator overrides from .bough.yaml. Every generated
// artifact records the Thresholds it was minted under so a later
// audit can reproduce the verdict.
type Thresholds struct {
	MemberMin          int     // GATE 1: minimum members for a cluster
	CohesionMin        float64 // GATE 2: minimum mean pairwise jaccard
	LexiconCoverageMax float64 // GATE 3': maximum prior-vocabulary coverage
	RelIsolationMin    float64 // GATE 4: minimum internal-vs-external isolation
}

// DefaultThresholds are the ECC v3 production values. Calibrated
// against the ~1k-instinct threecorp corpus; documented in
// docs/EVOLVE.md and the upstream evolve-skill-manual-v3 spec.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MemberMin:          2,
		CohesionMin:        0.20,
		LexiconCoverageMax: 0.55,
		RelIsolationMin:    0.40,
	}
}

// GateVerdict names which mechanical gate (if any) rejected a
// cluster, plus the metrics computed along the way. The metrics are
// surfaced in preview output + the GATE 5 judge prompt so the LLM
// sees the same numbers the mechanical gates did.
type GateVerdict struct {
	Passed          bool
	RejectedAt      string  // "" when passed; else "gate1" / "gate2" / "gate3" / "gate4"
	MemberCount     int
	Cohesion        float64
	LexiconCoverage float64
	MaxPriorOverlap float64
	RelIsolation    float64
	Reason          string
}

// EvaluateGates runs GATE 1 → GATE 4 over one cluster and returns
// the verdict. A passing verdict means the cluster is eligible for
// the GATE 5 LLM judge; a failing verdict names the gate that
// rejected it so the operator can see why a cluster did not become
// a skill.
//
// The gate order is load-bearing: GATE 1 (cheap member count) before
// GATE 2 (O(N²) cohesion) before GATE 3'/4 (prior comparisons). Short-
// circuiting at the first failure keeps a 1k-instinct evolve pass
// from computing cohesion for every singleton.
func EvaluateGates(c Cluster, th Thresholds) GateVerdict {
	v := GateVerdict{MemberCount: len(c.Members)}

	// GATE 1 — member count (hard).
	if len(c.Members) < th.MemberMin {
		v.RejectedAt = "gate1"
		v.Reason = "singleton or below member minimum; not a cluster"
		return v
	}

	// GATE 2 — cohesion (mean pairwise jaccard).
	v.Cohesion = meanPairwiseJaccard(c.memberTokens)
	if v.Cohesion < th.CohesionMin {
		v.RejectedAt = "gate2"
		v.Reason = "loose cluster: mean pairwise similarity below cohesion floor"
		return v
	}

	// GATE 3' — lexicon coverage vs all priors.
	priorAll := allPriorTokens(c)
	v.LexiconCoverage = coverage(c.candidateTokens, priorAll)
	if v.LexiconCoverage > th.LexiconCoverageMax {
		v.RejectedAt = "gate3"
		v.Reason = "candidate vocabulary already inside prior surface area: subdivision, not a new domain"
		return v
	}

	// GATE 4 — relative isolation.
	v.MaxPriorOverlap = c.NearestOverlap
	denom := v.Cohesion
	if denom < 0.001 {
		denom = 0.001
	}
	v.RelIsolation = (v.Cohesion - v.MaxPriorOverlap) / denom
	if v.RelIsolation < th.RelIsolationMin {
		v.RejectedAt = "gate4"
		v.Reason = "cannot cleanly separate from nearest prior: internal cohesion does not materially exceed outward overlap"
		return v
	}

	v.Passed = true
	v.Reason = "passed all mechanical gates; eligible for LLM semantic judge"
	return v
}

// meanPairwiseJaccard averages Jaccard over every i<j member pair.
// A single-member set returns 0 (= GATE 1 already rejected it, but
// the guard keeps the function total).
func meanPairwiseJaccard(sets []map[string]struct{}) float64 {
	n := len(sets)
	if n < 2 {
		return 0
	}
	sum := 0.0
	pairs := 0
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			sum += Jaccard(sets[i], sets[j])
			pairs++
		}
	}
	if pairs == 0 {
		return 0
	}
	return sum / float64(pairs)
}

// allPriorTokens unions the token sets of every prior the cluster
// knows about. GATE 3' measures candidate coverage against this
// union. When the cluster has no nearest prior (= first evolve pass)
// the union is empty and coverage is 0, so GATE 3' always passes —
// which is correct: with no priors, nothing can be a subdivision.
func allPriorTokens(c Cluster) map[string]struct{} {
	if c.NearestPrior == nil {
		return map[string]struct{}{}
	}
	// The cluster only caches its nearest prior; GATE 3' wants the
	// union of ALL priors. We approximate with the nearest prior's
	// tokens because Discover already filtered on max-prior overlap,
	// and the full prior union is threaded through SetPriorUnion when
	// the caller has it. See EvaluateGatesWithPriorUnion.
	return priorTokens(c.NearestPrior)
}

// EvaluateGatesWithPriorUnion is the precise GATE 3' variant: it
// measures lexicon coverage against the union of EVERY prior's
// tokens, not just the nearest one. The pipeline uses this when it
// has the full prior list; EvaluateGates is the convenience wrapper
// for callers (and tests) that only carry the nearest-prior link.
func EvaluateGatesWithPriorUnion(c Cluster, priorUnion map[string]struct{}, th Thresholds) GateVerdict {
	v := GateVerdict{MemberCount: len(c.Members)}
	if len(c.Members) < th.MemberMin {
		v.RejectedAt = "gate1"
		v.Reason = "singleton or below member minimum; not a cluster"
		return v
	}
	v.Cohesion = meanPairwiseJaccard(c.memberTokens)
	if v.Cohesion < th.CohesionMin {
		v.RejectedAt = "gate2"
		v.Reason = "loose cluster: mean pairwise similarity below cohesion floor"
		return v
	}
	v.LexiconCoverage = coverage(c.candidateTokens, priorUnion)
	if v.LexiconCoverage > th.LexiconCoverageMax {
		v.RejectedAt = "gate3"
		v.Reason = "candidate vocabulary already inside prior surface area: subdivision, not a new domain"
		return v
	}
	v.MaxPriorOverlap = c.NearestOverlap
	denom := v.Cohesion
	if denom < 0.001 {
		denom = 0.001
	}
	v.RelIsolation = (v.Cohesion - v.MaxPriorOverlap) / denom
	if v.RelIsolation < th.RelIsolationMin {
		v.RejectedAt = "gate4"
		v.Reason = "cannot cleanly separate from nearest prior: internal cohesion does not materially exceed outward overlap"
		return v
	}
	v.Passed = true
	v.Reason = "passed all mechanical gates; eligible for LLM semantic judge"
	return v
}
