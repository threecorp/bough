package inject

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// TestRelevanceFloorDropsIncidentalMatches covers the floor and its
// scaling in one pass, because the two only make sense together.
//
// A rich query gives BM25 many chances to score a candidate on one
// unremarkable word ("job"), and a single non-zero score used to be
// enough to take a line. The floor asks for two shared content tokens
// there. A terse query cannot supply two, so the same floor would make
// precise-but-short prompts return nothing — it scales down to one.
func TestRelevanceFloorDropsIncidentalMatches(t *testing.T) {
	corpus := []*homunculus.Instinct{
		{
			ID: "es-mapping-reindex", Confidence: 0.85,
			Trigger: "when the elasticsearch mapping changes",
			Body:    "## Action\nrebuild the elasticsearch index so the new mapping applies\n",
		},
		{
			ID: "cron-job-late", Confidence: 0.85,
			Trigger: "when a cron job is late",
			Body:    "## Action\ncheck the schedule\n",
		},
	}

	_, ids := Build(corpus, nil, Options{
		Prompt: "the elasticsearch reindex job finished, verify the mapping",
	})
	if !containsID(ids, "es-mapping-reindex") {
		t.Errorf("the on-topic instinct was dropped: %v", ids)
	}
	if containsID(ids, "cron-job-late") {
		t.Errorf("an instinct sharing only the word \"job\" cleared the floor: %v", ids)
	}

	// Same floor, terse query: one shared token is all it can ask for, so
	// the note it does name must come back.
	if _, ids = Build(corpus, nil, Options{Prompt: "cron job"}); !containsID(ids, "cron-job-late") {
		t.Errorf("a two-word prompt returned nothing it named: %v", ids)
	}
}

// TestExactHitBypassesTheFloor pins the exemption. Someone who typed the
// symbol has already said what they mean; asking them to ALSO share two
// prose words with the note would drop the single best hit in the corpus.
func TestExactHitBypassesTheFloor(t *testing.T) {
	corpus := []*homunculus.Instinct{
		{
			ID: "handler-guard", Confidence: 0.85,
			Trigger: "when Handler42 misbehaves",
			Body:    "## Action\nread the guard\n",
		},
	}
	// A long prompt, so the floor asks for two shared tokens — and the
	// note shares exactly one ("handler42"), which it carries as an
	// IDENTIFIER.
	_, ids := Build(corpus, nil, Options{
		Prompt: "anything covering pkg/module42.Handler42 for the migration rollback verification",
	})
	if !containsID(ids, "handler-guard") {
		t.Errorf("an exact identifier hit was dropped by the shared-token floor: %v", ids)
	}
}

// TestAnOversizedLineDoesNotDiscardTheRest is the skip-not-stop rule. One
// instinct whose action had collapsed onto a single very long line ranked
// first, and the block came back holding only that — or, once a cap was
// enforced, holding nothing at all. Either way the prompt read it as
// "nothing was relevant".
func TestAnOversizedLineDoesNotDiscardTheRest(t *testing.T) {
	corpus := []*homunculus.Instinct{
		mkI("giant", 0.85, strings.Repeat("collapsed onto one line ", 40)),
		mkI("normal-a", 0.85, "run bash -n after each edit"),
		mkI("normal-b", 0.85, "describe the image before deploying"),
		mkI("normal-c", 0.85, "compare column positions with awk"),
	}
	// No prompt: the fallback order is alphabetical, so "giant" is first.
	block, ids := Build(corpus, nil, Options{MaxBytes: 300})
	if containsID(ids, "giant") {
		t.Errorf("a line that cannot fit the budget was rendered anyway: %v", ids)
	}
	if len(ids) != 3 {
		t.Errorf("rendered = %d, want the 3 that fit (ids: %v)\n%s", len(ids), ids, block)
	}
	if len(block) > 300 {
		t.Errorf("block = %d bytes, over the %d cap", len(block), 300)
	}
}

// TestFilteredCandidatesAreReplaced pins that MaxInstincts caps the
// OUTPUT rather than the candidate pool. When it capped the pool first,
// every candidate the floor / family cap / restatement check dropped
// shrank the block by one — the filters quietly cost delivery instead of
// improving it.
func TestFilteredCandidatesAreReplaced(t *testing.T) {
	var corpus []*homunculus.Instinct
	// Five mutual restatements, alphabetically first so they are
	// considered before anything else.
	for i := 0; i < 5; i++ {
		corpus = append(corpus, mkI(fmt.Sprintf("aaa-copy-%d", i), 0.85,
			"always prefix grep with the command keyword to bypass the alias"))
	}
	for i := 0; i < 20; i++ {
		corpus = append(corpus, mkI(fmt.Sprintf("note-%02d", i), 0.85,
			fmt.Sprintf("do the %d-th distinct thing", i)))
	}
	_, ids := Build(corpus, nil, Options{})
	if len(ids) != DefaultMaxInstincts {
		t.Errorf("rendered = %d, want a full block of %d (ids: %v)", len(ids), DefaultMaxInstincts, ids)
	}
	copies := 0
	for _, id := range ids {
		if strings.HasPrefix(id, "aaa-copy-") {
			copies++
		}
	}
	if copies != 1 {
		t.Errorf("restatements rendered = %d, want 1 (ids: %v)", copies, ids)
	}
}
