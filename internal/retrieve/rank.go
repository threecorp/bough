package retrieve

import (
	"math"
	"sort"
	"time"
)

// Doc is one retrievable item. Text is everything searchable about it
// (id + trigger + body); ModTime feeds the recency prior. Confidence is
// carried for display and audit — deliberately NOT a ranking input.
type Doc struct {
	ID         string
	Text       string
	ModTime    time.Time
	Confidence float64
	IsProject  bool
}

// Result is one ranked doc plus why it survived, so a caller (or an
// operator debugging an empty block) can see which channel matched
// rather than guessing at a single opaque score.
type Result struct {
	Doc      Doc
	Score    float64
	InExact  bool
	InLexPri bool
}

// Ranker fuses the three channels. The BM25 constants and the RRF k live
// on the struct rather than at package scope: they are tuning knobs, so
// a test can construct a Ranker with different values instead of the
// package carrying names that read as global configuration.
type Ranker struct {
	// K1 and B are the standard BM25 term-saturation and
	// length-normalisation parameters.
	K1 float64
	B  float64
	// RRFk damps the contribution of low ranks in Reciprocal Rank
	// Fusion; 60 is the value from the original paper and behaves well
	// without per-corpus calibration.
	RRFk float64
	// RecencyWeight scales the recency channel's contribution. It is
	// deliberately below 1: recency is a weak PRIOR, not evidence of
	// relevance. At equal weight a merely-recent note ties with a note
	// the prompt actually matched, and the tiebreak — not the ranking —
	// decides, which is the same failure this package exists to fix.
	RecencyWeight float64
	// ChannelLimit is the per-channel candidate DEPTH: only a channel's
	// top-N ranks reach the fusion. It bounds the work fusion does on a
	// large corpus, and it is what makes the fused score mean "several
	// channels agree near the top" rather than "some channel found this
	// somewhere". Without it a doc that ranked 800th lexically still
	// entered the pool carrying a near-zero RRF term, and the ordering
	// among such candidates was decided by the tiebreak again.
	//
	// It applies to the recency channel too: a weak prior computed over
	// the whole corpus would hand every candidate the same tiny bonus,
	// which is not a prior, it is a constant. Zero means "no bound".
	ChannelLimit int
	// MaxResults bounds the returned slice. Zero means "no bound".
	MaxResults int
}

// NewRanker returns a Ranker with the published defaults.
func NewRanker() *Ranker {
	return &Ranker{K1: 1.2, B: 0.75, RRFk: 60, RecencyWeight: 0.3, ChannelLimit: 50, MaxResults: 0}
}

// Rank scores docs against the query text plus any context tokens the
// caller derived from the environment (see ContextTokens).
//
// A doc that appears in NEITHER the exact nor the lexical channel is
// dropped, even when recency would rank it: that is what lets an
// off-topic prompt correctly return nothing instead of filling the
// budget with whatever was written most recently.
func (r *Ranker) Rank(docs []Doc, query string, contextTokens []string) []Result {
	if len(docs) == 0 {
		return nil
	}
	// The lexical channel scores against CONTENT tokens: sharing "in" or
	// "the" with a document is not evidence, and BM25 would still give
	// such a match a small non-zero score — which is exactly what the
	// drop rule treats as "this candidate matched".
	queryTokens := ContentTokens(query)
	queryIdents := Identifiers(query)
	// Context tokens describe WHERE the session is working (a sub-project
	// or package name), so they feed both channels: they are identifier-
	// shaped by nature and they are also legitimate lexical evidence.
	for _, t := range contextTokens {
		for tok := range ContentTokens(t) {
			queryTokens[tok] = struct{}{}
		}
		for ident := range Identifiers(t) {
			queryIdents[ident] = struct{}{}
		}
	}
	if len(queryTokens) == 0 && len(queryIdents) == 0 {
		return nil
	}

	exactRank := capDepth(rankByScore(exactScores(docs, queryIdents)), r.ChannelLimit)
	lexRank := capDepth(rankByScore(r.bm25Scores(docs, queryTokens)), r.ChannelLimit)
	recRank := capDepth(recencyRanks(docs), r.ChannelLimit)

	out := make([]Result, 0, len(docs))
	for i, d := range docs {
		er, inExact := exactRank[i]
		lr, inLex := lexRank[i]
		if !inExact && !inLex {
			continue // recency alone never qualifies a candidate
		}
		score := 0.0
		if inExact {
			score += 1 / (r.RRFk + float64(er))
		}
		if inLex {
			score += 1 / (r.RRFk + float64(lr))
		}
		if rr, ok := recRank[i]; ok {
			score += r.RecencyWeight / (r.RRFk + float64(rr))
		}
		out = append(out, Result{Doc: d, Score: score, InExact: inExact, InLexPri: inLex})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		// Deterministic tiebreak: project before global, then id. This
		// is a TIEBREAK, not the ranking — the bug being fixed here is a
		// tiebreak that had become the ranking because every score was
		// identical.
		if out[i].Doc.IsProject != out[j].Doc.IsProject {
			return out[i].Doc.IsProject
		}
		return out[i].Doc.ID < out[j].Doc.ID
	})
	if r.MaxResults > 0 && len(out) > r.MaxResults {
		out = out[:r.MaxResults]
	}
	return out
}

// exactScores counts how many query identifiers each doc names. Docs
// with no hit are absent from the map (not zero-scored), because
// "absent" is what the drop rule keys on.
func exactScores(docs []Doc, queryIdents map[string]struct{}) map[int]float64 {
	scores := map[int]float64{}
	if len(queryIdents) == 0 {
		return scores
	}
	for i, d := range docs {
		hits := 0.0
		docIdents := Identifiers(d.Text)
		for ident := range queryIdents {
			if _, ok := docIdents[ident]; ok {
				hits++
			}
		}
		if hits > 0 {
			scores[i] = hits
		}
	}
	return scores
}

// bm25Scores ranks docs by Okapi BM25 over the query tokens. Docs
// scoring zero are absent from the map for the same reason as above.
func (r *Ranker) bm25Scores(docs []Doc, queryTokens map[string]struct{}) map[int]float64 {
	n := float64(len(docs))
	docTokens := make([]map[string]int, len(docs))
	totalLen := 0.0
	for i, d := range docs {
		tf := map[string]int{}
		length := 0
		for tok := range ContentTokens(d.Text) {
			tf[tok]++
			length++
		}
		docTokens[i] = tf
		totalLen += float64(length)
	}
	avgdl := totalLen / n
	if avgdl == 0 {
		avgdl = 1
	}
	// Document frequency per query term.
	df := map[string]float64{}
	for tok := range queryTokens {
		for _, tf := range docTokens {
			if tf[tok] > 0 {
				df[tok]++
			}
		}
	}
	scores := map[int]float64{}
	for i, tf := range docTokens {
		length := 0.0
		for _, c := range tf {
			length += float64(c)
		}
		total := 0.0
		for tok := range queryTokens {
			f := float64(tf[tok])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (n-df[tok]+0.5)/(df[tok]+0.5))
			total += idf * (f * (r.K1 + 1)) / (f + r.K1*(1-r.B+r.B*length/avgdl))
		}
		if total > 0 {
			scores[i] = total
		}
	}
	return scores
}

// recencyRanks ranks every doc by modification time, newest first. Every
// doc is present — recency is a prior over the survivors, not a filter.
func recencyRanks(docs []Doc) map[int]int {
	idx := make([]int, len(docs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return docs[idx[a]].ModTime.After(docs[idx[b]].ModTime)
	})
	ranks := make(map[int]int, len(docs))
	for rank, i := range idx {
		ranks[i] = rank + 1
	}
	return ranks
}

// capDepth drops every entry ranked deeper than limit, so only a
// channel's head reaches the fusion. A non-positive limit means "no
// bound" and returns the ranks unchanged.
//
// It returns a NEW map rather than deleting from the argument: the
// caller's map is derived per call today, but a helper that quietly
// mutates what it is handed is the kind of thing a later caller
// discovers the hard way.
func capDepth(ranks map[int]int, limit int) map[int]int {
	if limit <= 0 {
		return ranks
	}
	out := make(map[int]int, len(ranks))
	for i, rank := range ranks {
		if rank <= limit {
			out[i] = rank
		}
	}
	return out
}

// rankByScore converts a sparse score map into 1-based ranks, highest
// score first, breaking ties by doc index so the ranking is total and
// reproducible.
func rankByScore(scores map[int]float64) map[int]int {
	idx := make([]int, 0, len(scores))
	for i := range scores {
		idx = append(idx, i)
	}
	sort.Slice(idx, func(a, b int) bool {
		if scores[idx[a]] != scores[idx[b]] {
			return scores[idx[a]] > scores[idx[b]]
		}
		return idx[a] < idx[b]
	})
	ranks := make(map[int]int, len(idx))
	for rank, i := range idx {
		ranks[i] = rank + 1
	}
	return ranks
}
