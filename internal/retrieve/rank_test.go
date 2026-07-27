package retrieve

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// corpus builds n synthetic instincts whose ids sort alphabetically and
// whose text is distinctive per item. It reproduces the shape that made
// confidence-sorting fail on the real corpus: every doc equally
// "confident", so any confidence-based order is decided by the tiebreak.
func corpus(n int) []Doc {
	docs := make([]Doc, 0, n)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%c%c-topic-%02d", 'a'+i/26, 'a'+i%26, i)
		docs = append(docs, Doc{
			ID:         id,
			Text:       fmt.Sprintf("%s when working on subsystem%02d call Handler%02d in module%02d", id, i, i, i),
			ModTime:    base.Add(time.Duration(i) * time.Hour),
			Confidence: 0.85, // the measured real-corpus value: identical for all
			IsProject:  true,
		})
	}
	return docs
}

// TestSelfRetrieval_NoStarvation is the regression net the whole change
// is judged by, and it is written to fail loudly if ranking ever stops
// consuming the prompt.
//
// For each instinct, build a query out of that instinct's own content
// and assert it comes back in the top K. Under a content-independent
// order (confidence tie → id ascending → truncate at N) only the first N
// ids can ever be returned, so the pass rate is pinned at N/total no
// matter what the prompt says.
func TestSelfRetrieval_NoStarvation(t *testing.T) {
	const (
		total = 120
		topK  = 10
	)
	docs := corpus(total)
	r := NewRanker()
	r.MaxResults = topK

	hits := 0
	var misses []string
	for _, d := range docs {
		// A query phrased from the doc's own content — the way a person
		// would refer to it — not the doc's text verbatim.
		query := fmt.Sprintf("how do I use %s", strings.Fields(d.Text)[6])
		found := false
		for _, res := range r.Rank(docs, query, nil) {
			if res.Doc.ID == d.ID {
				found = true
				break
			}
		}
		if found {
			hits++
		} else if len(misses) < 5 {
			misses = append(misses, d.ID+" (query: "+query+")")
		}
	}
	rate := float64(hits) / float64(total)
	if rate < 0.99 {
		t.Errorf("self-retrieval rate = %.1f%% (%d/%d), want >= 99%%\nfirst misses: %v",
			rate*100, hits, total, misses)
	}
}

// TestConfidenceIsNotARankingKey pins the invariant directly: a
// low-confidence doc that the prompt actually names must outrank a
// high-confidence doc it does not. Confidence stays stored for audit;
// letting it order results is the original defect.
func TestConfidenceIsNotARankingKey(t *testing.T) {
	docs := []Doc{
		{ID: "irrelevant-but-certain", Text: "when deploying charts run the sync", Confidence: 1.0, IsProject: true},
		{ID: "relevant-but-unsure", Text: "when the mysql handshake fails retry for 30s", Confidence: 0.1, IsProject: true},
	}
	got := NewRanker().Rank(docs, "mysql handshake keeps failing", nil)
	if len(got) == 0 {
		t.Fatal("expected the relevant doc to be returned")
	}
	if got[0].Doc.ID != "relevant-but-unsure" {
		t.Errorf("top result = %q, want the prompt-relevant doc regardless of confidence", got[0].Doc.ID)
	}
}

// TestOffTopicPromptReturnsNothing pins the drop rule: recency alone
// must never qualify a candidate, so an unrelated prompt correctly
// yields zero results instead of the most recently written notes.
func TestOffTopicPromptReturnsNothing(t *testing.T) {
	docs := corpus(20)
	got := NewRanker().Rank(docs, "photosynthesis in alpine wildflowers", nil)
	if len(got) != 0 {
		ids := make([]string, 0, len(got))
		for _, g := range got {
			ids = append(ids, g.Doc.ID)
		}
		t.Errorf("off-topic prompt returned %d results (%v), want 0", len(got), ids)
	}
}

// TestEmptyQueryReturnsNothing pins the degenerate input: with no prompt
// there is no relevance to compute, so the caller must fall back rather
// than receive an arbitrary order dressed up as a ranking.
func TestEmptyQueryReturnsNothing(t *testing.T) {
	if got := NewRanker().Rank(corpus(5), "", nil); len(got) != 0 {
		t.Errorf("empty query returned %d results, want 0", len(got))
	}
}

// --- the four unsatisfiable-by-construction bugs, as tests ---
//
// Each of these produced ZERO results rather than wrong ones, which is
// why they survived: the system looked like it simply had nothing to
// say. They are pinned here so a future tokenizer change cannot quietly
// reintroduce one.

// Bug 1: an identifier indexed with its call arguments never matches a
// person typing the bare name.
func TestIdentifierWithCallArgsMatchesBareName(t *testing.T) {
	docs := []Doc{{
		ID:        "capability-check",
		Text:      "when gating a feature call AssertCapability(ctx, capID) before the write",
		IsProject: true,
	}}
	got := NewRanker().Rank(docs, "where should I call AssertCapability", nil)
	if len(got) == 0 || !got[0].InExact {
		t.Errorf("bare identifier query must hit the exact channel, got %+v", got)
	}
}

// Bug 2: a dotted query token compared against a tokenizer that strips
// dots can never be satisfied — the dotted form must survive on both
// sides, and so must its segments.
func TestDottedIdentifierIsSatisfiable(t *testing.T) {
	docs := []Doc{{
		ID:        "layout-paths",
		Text:      "the on-disk paths come from homunculus.Layout, never build them by hand",
		IsProject: true,
	}}
	for _, q := range []string{"homunculus.Layout", "Layout"} {
		got := NewRanker().Rank(docs, "which type owns "+q, nil)
		if len(got) == 0 {
			t.Errorf("query %q returned nothing; dotted tokens must match whole and by segment", q)
		}
	}
}

// Bug 3: dropping ≤2-char tokens on both sides makes a query whose only
// content words are short unanswerable by construction.
func TestShortTokensRemainAnswerable(t *testing.T) {
	docs := []Doc{{
		ID:        "pr-conventions",
		Text:      "keep the PR body short: a table and a few bullets, no long test plan",
		IsProject: true,
	}}
	got := NewRanker().Rank(docs, "PR body conventions", nil)
	if len(got) == 0 {
		t.Error("a query whose content word is a 2-char token must still be answerable")
	}
}

// Bug 4: deriving context tokens from the raw cwd basename means that at
// the repo root the token IS the repo name, which matches a large share
// of the corpus and drowns short prompts.
func TestContextTokensAtRootAreEmpty(t *testing.T) {
	if got := ContextTokens("/src/myrepo", "/src/myrepo"); len(got) != 0 {
		t.Errorf("at the project root context tokens = %v, want none (the repo name carries no signal)", got)
	}
	got := ContextTokens("/src/myrepo", "/src/myrepo/services/billing")
	if len(got) != 2 || got[0] != "services" || got[1] != "billing" {
		t.Errorf("below the root context tokens = %v, want [services billing]", got)
	}
	if got := ContextTokens("/src/myrepo", "/elsewhere"); len(got) != 0 {
		t.Errorf("outside the root context tokens = %v, want none", got)
	}
}

// TestContextTokensNarrowResults pins that the surviving context signal
// is actually used: two otherwise-equal notes should be separated by
// which sub-project the session is in.
func TestContextTokensNarrowResults(t *testing.T) {
	docs := []Doc{
		{ID: "billing-note", Text: "run the reconciliation job after a schema change", IsProject: true},
		{ID: "search-note", Text: "run the reindex job after a schema change", IsProject: true},
	}
	got := NewRanker().Rank(docs, "after a schema change", []string{"reindex"})
	if len(got) == 0 || got[0].Doc.ID != "search-note" {
		t.Errorf("context tokens should lift the matching note, got %+v", got)
	}
}
