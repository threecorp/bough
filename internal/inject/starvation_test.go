package inject

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// starvationCorpus is the adversarial shape the C-1 regression net is
// measured on, built to make the original failure reproducible rather
// than trivially absent:
//
//   - ten FAMILIES of twelve near-duplicates each, sharing their
//     vocabulary and differing in one distinguishing token (the real
//     corpus grows such families — the observer mints a fresh note for
//     the same lesson every time it recurs),
//   - thirty singletons with identifier-bearing triggers,
//   - uniform confidence, so confidence order is no help,
//   - enough volume that budget pressure is real.
//
// The reference selector shipped with 21 of 1300 notes reachable:
// confidence was a near-tie, so the real order was the id tiebreak, and
// the byte budget cut after the alphabet's first letters. Everything from
// 'c' onward was unreachable at ANY relevance, forever.
func starvationCorpus() []*homunculus.Instinct {
	var corpus []*homunculus.Instinct
	mk := func(id, trigger, action string) {
		corpus = append(corpus, &homunculus.Instinct{
			ID:         id,
			Trigger:    trigger,
			Body:       "## Action\n" + action + "\n",
			Confidence: 0.85, // uniform on purpose: the tie is the trap
		})
	}

	families := []struct{ topic, verb string }{
		{"background task output polling", "poll the output file"},
		{"bash diagnostic section markers", "prefix sections with echo markers"},
		{"scratchpad write then read cycle", "write output to a scratchpad file"},
		{"shell script syntax validation", "run bash -n after each edit"},
		{"grep command prefix alias bypass", "prefix grep with the command keyword"},
		{"environment selective execution guard", "guard sections on a target env"},
		{"isolated homunculus test sandbox", "export the sandbox env vars"},
		{"git show redirect before filtering", "redirect git show to a file"},
		{"ecr image existence verification", "describe the image before deploy"},
		{"column alignment awk verification", "compare column positions with awk"},
	}
	variants := []string{
		"loops", "harvest", "inspection", "notification", "marker files",
		"long builds", "flaky suites", "ci pipelines", "release waits",
		"api queries", "rollout checks", "queue drains",
	}
	for fi, f := range families {
		for vi, v := range variants {
			id := fmt.Sprintf("family%02d-%s-%02d", fi, firstWord(f.topic), vi)
			mk(id,
				fmt.Sprintf("when handling %s during %s", f.topic, v),
				fmt.Sprintf("%s (%s case %d)", f.verb, v, vi))
		}
	}
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("single%02d", i)
		mk(id,
			fmt.Sprintf("when pkg/module%02d.Handler%02d misbehaves in prod%02d", i, i, i),
			fmt.Sprintf("read pkg/module%02d/handler.go and check Handler%02d's guard", i, i))
	}
	return corpus
}

// TestNoUniqueInstinctIsStarved is the C-1 bar: for every instinct whose
// knowledge exists nowhere else in the corpus, a query built from its own
// trigger must retrieve IT. ≥99%.
//
// Measured on this corpus: 30/30 singletons. With the near-duplicate skip
// disabled the family members score 120/120 as well, so nothing here is
// unreachable by its own content — the 30 that lose to a sibling once the
// skip is ON are the subject of the next test, and they are the skip
// working, not starvation.
func TestNoUniqueInstinctIsStarved(t *testing.T) {
	corpus := starvationCorpus()
	unique, starved := 0, 0
	var examples []string
	for _, in := range corpus {
		if !strings.HasPrefix(in.ID, "single") {
			continue // a member of a near-duplicate family; see below
		}
		unique++
		_, ids := Build(corpus, nil, Options{Prompt: in.Trigger})
		if !containsID(ids, in.ID) {
			starved++
			if len(examples) < 5 {
				examples = append(examples, in.ID)
			}
		}
	}
	if rate := float64(unique-starved) / float64(unique) * 100; rate < 99.0 {
		t.Errorf("self-retrieval %.1f%% (<99%%): %d of %d unique instincts starved, e.g. %v",
			rate, starved, unique, examples)
	}
}

// TestNoFamilyIsSilenced is the bar for the twelve-way near-duplicate
// families, and it states what "not starved" can honestly mean once
// restatements are skipped.
//
// A note whose own query returns a SIBLING saying the same thing is not
// starved — the knowledge reached the prompt. Demanding the note itself
// would be demanding that the skip not work: measured, 30 of 120 family
// members lose their own query to a sibling, and every one of those
// queries still delivered that family's advice.
//
// What must never happen is a family's own query returning nothing from
// that family at all. That is starvation, and it is what this asserts.
func TestNoFamilyIsSilenced(t *testing.T) {
	corpus := starvationCorpus()
	members, silent, self := 0, 0, 0
	var examples []string
	for _, in := range corpus {
		if !strings.HasPrefix(in.ID, "family") {
			continue
		}
		members++
		family := in.ID[:len("family00")]
		_, ids := Build(corpus, nil, Options{Prompt: in.Trigger})
		if containsID(ids, in.ID) {
			self++
		}
		delivered := false
		for _, id := range ids {
			if strings.HasPrefix(id, family) {
				delivered = true
				break
			}
		}
		if !delivered {
			silent++
			if len(examples) < 5 {
				examples = append(examples, in.ID)
			}
		}
	}
	if silent > 0 {
		t.Errorf("%d of %d family queries returned nothing from their own family, e.g. %v",
			silent, members, examples)
	}
	// The other direction, so this test cannot pass by the skip having
	// quietly stopped firing: if every member retrieved itself, twelve
	// ways of saying one thing are back in the budget.
	if self == members {
		t.Errorf("all %d near-duplicate members retrieved THEMSELVES — the restatement skip is not firing", members)
	}
}

// TestRestatementsAreSkipped pins the skip directly rather than through
// the corpus statistics above: two notes saying the same thing in the
// same words must not both take a line.
func TestRestatementsAreSkipped(t *testing.T) {
	corpus := []*homunculus.Instinct{
		mkI("first-copy", 0.85, "always run bash -n on the script after editing it"),
		mkI("second-copy", 0.85, "always run bash -n on the script after editing it"),
		mkI("unrelated", 0.85, "describe the ECR image before deploying it"),
	}
	const prompt = "editing a shell script, should I run bash -n on it"
	_, ids := Build(corpus, nil, Options{Prompt: prompt})
	copies := 0
	for _, id := range ids {
		if strings.HasSuffix(id, "-copy") {
			copies++
		}
	}
	if copies != 1 {
		t.Errorf("restatements rendered = %d, want 1 (ids: %v)", copies, ids)
	}
	// And the bar is a knob, not a law: raised above 1 nothing counts as a
	// restatement, which is how the skip is shown to be what did it.
	if _, both := Build(corpus, nil, Options{Prompt: prompt, NearDupJaccard: 1.01}); len(both) < 2 {
		t.Errorf("with the bar disabled both copies must render, got %v", both)
	}
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func firstWord(s string) string {
	for i, r := range s {
		if r == ' ' {
			return s[:i]
		}
	}
	return s
}
