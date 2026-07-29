package inject

import (
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// crowdedCorpus is four instincts: three that restate one subject (one
// family) and one about something else. It is the shape the cap exists
// for — without it the family takes the whole block and the fourth
// subject, which the prompt also touched, is never delivered.
func crowdedCorpus() []*homunculus.Instinct {
	return []*homunculus.Instinct{
		mkI("poll-a", 0.85, "poll the background task output"),
		mkI("poll-b", 0.85, "poll the background task output again"),
		mkI("poll-c", 0.85, "poll the background task output once more"),
		mkI("other-subject", 0.85, "run the migration before the deploy"),
	}
}

func TestClusterCapTrimsOneFamily(t *testing.T) {
	got, ids := Build(crowdedCorpus(), nil, Options{
		ClusterOf: map[string]int{"poll-a": 0, "poll-b": 0, "poll-c": 0, "other-subject": 1},
	})
	polls := 0
	for _, id := range ids {
		if strings.HasPrefix(id, "poll-") {
			polls++
		}
	}
	if polls != DefaultClusterCap {
		t.Errorf("family members rendered = %d, want %d\n%s", polls, DefaultClusterCap, got)
	}
	// The point of trimming is what it makes room for.
	if !strings.Contains(got, "run the migration") {
		t.Errorf("the other subject was crowded out:\n%s", got)
	}
}

// TestUnstampedCorpusIsUncapped is the other direction, and it is the
// one that matters most: a corpus no evolve pass has stamped must be
// UNCAPPED rather than capped-at-one. Guessing that an unstamped
// instinct is alone in its family would silently drop instincts on
// information the file does not carry.
func TestUnstampedCorpusIsUncapped(t *testing.T) {
	_, ids := Build(crowdedCorpus(), nil, Options{})
	if len(ids) != 4 {
		t.Errorf("rendered = %d, want all 4 when nothing is stamped (ids: %v)", len(ids), ids)
	}
}

// TestPartialStampCapsOnlyStampedFamilies pins the mixed state a real
// corpus is always in: the last pass stamped the families it discovered,
// and everything minted since is unstamped.
func TestPartialStampCapsOnlyStampedFamilies(t *testing.T) {
	_, ids := Build(crowdedCorpus(), nil, Options{
		ClusterOf: map[string]int{"poll-a": 0, "poll-b": 0, "poll-c": 0},
	})
	if len(ids) != 3 { // 2 of the family + the unstamped one
		t.Errorf("rendered = %d, want 3 (ids: %v)", len(ids), ids)
	}
	joined := strings.Join(ids, ",")
	if !strings.Contains(joined, "other-subject") {
		t.Errorf("the unstamped instinct was dropped: %v", ids)
	}
}
