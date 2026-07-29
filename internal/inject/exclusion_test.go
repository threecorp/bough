package inject

import (
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

func inst(id, action string) *homunculus.Instinct {
	return &homunculus.Instinct{
		ID: id, Trigger: "when " + id, Confidence: 0.9,
		Body: "## Action\n" + action + "\n",
	}
}

// TestExcludeIDsDropsSkillCoveredInstincts pins the single consumer of
// the coverage registry: an instinct an evolved skill already delivers
// is not ALSO pushed into the prompt.
func TestExcludeIDsDropsSkillCoveredInstincts(t *testing.T) {
	project := []*homunculus.Instinct{
		inst("covered-by-skill", "Run the reindex job."),
		inst("not-covered", "Read the enclosing function first."),
	}
	block, ids := Build(project, nil, Options{
		ExcludeIDs: map[string]struct{}{"covered-by-skill": {}},
	})
	if len(ids) != 1 {
		t.Fatalf("included = %d, want 1 (the uncovered instinct only)", len(ids))
	}
	if strings.Contains(block, "reindex") {
		t.Errorf("a skill-covered instinct was also pushed:\n%s", block)
	}
	if !strings.Contains(block, "enclosing function") {
		t.Errorf("the uncovered instinct must still be pushed:\n%s", block)
	}
}

// TestExcludeIDsAppliesToGlobalScope pins that coverage is scope-blind:
// the same id promoted to global is delivered by the same skill, so it
// must not slip back in through the global pool.
func TestExcludeIDsAppliesToGlobalScope(t *testing.T) {
	global := []*homunculus.Instinct{inst("covered-by-skill", "Run the reindex job.")}
	_, ids := Build(nil, global, Options{
		ExcludeIDs: map[string]struct{}{"covered-by-skill": {}},
	})
	if len(ids) != 0 {
		t.Errorf("included = %d, want 0 — a covered id must not return via global scope", len(ids))
	}
}

// TestNoExcludeIDsPushesEverything pins the DEFAULT. Suppressing the
// push for knowledge whose pull path has not demonstrably fired removes
// it from both paths at once, so the caller must opt in — with no
// exclusion set, nothing is suppressed.
func TestNoExcludeIDsPushesEverything(t *testing.T) {
	project := []*homunculus.Instinct{
		inst("covered-by-skill", "Run the reindex job."),
		inst("not-covered", "Read the enclosing function first."),
	}
	if _, ids := Build(project, nil, Options{}); len(ids) != 2 {
		t.Errorf("included = %d, want 2 — exclusion must be opt-in", len(ids))
	}
}
