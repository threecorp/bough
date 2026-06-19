package api

// EvaluationOutcome is the evaluator's coarse-grained verdict. Fine-
// grained signals (utility breakdown, regression evidence) live on
// EvaluateResp fields below; Outcome is the routing decision the
// host's coordinator uses.
type EvaluationOutcome string

const (
	OutcomeUnspecified EvaluationOutcome = ""
	OutcomePromote     EvaluationOutcome = "promote"
	OutcomeKeep        EvaluationOutcome = "keep"
	OutcomeRevise      EvaluationOutcome = "revise"
	OutcomePrune       EvaluationOutcome = "prune"
)

// EvaluateReq passes the artifact to score. EvaluatorContextJSON is
// a free-form plugin-defined input slot (e.g., a comparison
// trajectory the evaluator should compare against).
type EvaluateReq struct {
	// ArtifactID and the matching artifact bytes are passed
	// separately so the evaluator does not need to import
	// plugins/capability/api just to read the request. The host
	// serialises the artifact as JSON before invoking Evaluate.
	ArtifactID            string
	ArtifactJSON          []byte
	EvaluatorContextJSON  []byte
}

type EvaluateResp struct {
	Outcome             EvaluationOutcome
	Utility             float64 // [0.0, 1.0], evaluator's confidence
	ConfidenceDelta     float64 // suggested confidence adjustment
	ShouldPrune         bool    // OutcomePrune convenience flag
	EvidenceRefs        []string
	Explanation         string // human-readable reasoning
	EvaluatorPayloadJSON []byte // free-form plugin-defined output
}
