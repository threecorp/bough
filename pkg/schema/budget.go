package schema

// RetrieveBudget is the running token / result budget the
// coordinator enforces while iterating QueryResult rows. Round 3
// AI #1 made this explicit: backends return EstimatedTokens and
// Truncated on each result; the coordinator stops iterating once
// either the running token total or the result count exceeds the
// configured retrieve.max_tokens / retrieve.max_results.
//
// The aggregator pattern keeps the budget logic in one place even
// when results come from multiple scope tiers (worktree + repo +
// global) and possibly multiple backends. The coordinator creates
// one RetrieveBudget per `bough instinct query`, then calls
// Allow(result) on each candidate before deciding to include it.
type RetrieveBudget struct {
	MaxResults int
	MaxTokens  int

	UsedResults int
	UsedTokens  int

	// AnyTruncated records whether at least one result the backend
	// returned had Truncated=true. The host surfaces this in the
	// CLI output so users can see "1 of 12 results truncated at
	// 4000 tokens" rather than silently seeing a partial answer.
	AnyTruncated bool
}

// Allow returns true if a result with the given estimated token
// cost still fits within the budget. The caller is expected to
// invoke Consume() if the result is actually included.
func (b *RetrieveBudget) Allow(estimatedTokens int, truncated bool) bool {
	if b.MaxResults > 0 && b.UsedResults >= b.MaxResults {
		return false
	}
	if b.MaxTokens > 0 && b.UsedTokens+estimatedTokens > b.MaxTokens {
		return false
	}
	_ = truncated // recorded only on Consume
	return true
}

// Consume records that a result has been included. Truncated rolls
// up into AnyTruncated for the post-query summary.
func (b *RetrieveBudget) Consume(estimatedTokens int, truncated bool) {
	b.UsedResults++
	b.UsedTokens += estimatedTokens
	if truncated {
		b.AnyTruncated = true
	}
}

// Remaining returns (results, tokens) headroom. Either field may be
// zero with the cap unset (= unlimited); the caller should not
// inspect those fields without consulting Max* in parallel.
func (b *RetrieveBudget) Remaining() (results, tokens int) {
	results = b.MaxResults - b.UsedResults
	tokens = b.MaxTokens - b.UsedTokens
	if results < 0 {
		results = 0
	}
	if tokens < 0 {
		tokens = 0
	}
	return results, tokens
}
