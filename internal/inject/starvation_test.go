package inject

import (
	"fmt"
	"testing"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// TestNoInstinctIsStarved is the C-1 regression net, and the contract
// says it goes in BEFORE any ranking change: for every note, a query
// built from its own trigger must retrieve the note itself. The
// reference system shipped a selector where 21 notes of 1300 were
// reachable — confidence was a near-tie, the real order was the id
// tiebreak, and the byte budget cut after the alphabet's first letters.
// Everything from 'c' onward was unreachable at ANY relevance, forever.
//
// The corpus is built to make that failure reproducible rather than
// trivially absent: FAMILIES of near-duplicate notes sharing their
// vocabulary (the real corpus grows 8-variant families of the same
// habit), uniform confidence (so confidence order is no help), and
// enough volume that budget pressure is real. Measured on the live
// 247-note corpus before any Phase B change: 247/247.
func TestNoInstinctIsStarved(t *testing.T) {
	var corpus []*homunculus.Instinct
	mk := func(id, trigger, action string) {
		corpus = append(corpus, &homunculus.Instinct{
			ID:         id,
			Trigger:    trigger,
			Body:       "## Action\n" + action + "\n",
			Confidence: 0.85, // uniform on purpose: the tie is the trap
		})
	}

	// Ten families of twelve near-duplicates each. Members share the
	// family vocabulary and differ in one distinguishing token — the
	// exact shape where a family crowds the budget and the tail starves.
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
	// Thirty singletons with identifier-bearing triggers — the easy
	// class, present so the corpus is not one giant blur.
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("single%02d", i)
		mk(id,
			fmt.Sprintf("when pkg/module%02d.Handler%02d misbehaves in prod%02d", i, i, i),
			fmt.Sprintf("read pkg/module%02d/handler.go and check Handler%02d's guard", i, i))
	}

	starved := 0
	var examples []string
	for _, in := range corpus {
		_, ids := Build(corpus, nil, Options{Prompt: in.Trigger})
		found := false
		for _, id := range ids {
			if id == in.ID {
				found = true
				break
			}
		}
		if !found {
			starved++
			if len(examples) < 5 {
				examples = append(examples, in.ID)
			}
		}
	}
	rate := float64(len(corpus)-starved) / float64(len(corpus)) * 100
	if rate < 99.0 {
		t.Errorf("self-retrieval %.1f%% (<99%%): %d of %d starved, e.g. %v",
			rate, starved, len(corpus), examples)
	}
}

func firstWord(s string) string {
	for i, r := range s {
		if r == ' ' {
			return s[:i]
		}
	}
	return s
}
