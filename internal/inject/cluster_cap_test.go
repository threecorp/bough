package inject

import (
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// crowdedCorpus is four instincts: three that belong to ONE family (all
// about waiting on something slow) plus one unrelated. Their wording is
// deliberately distinct — pairwise Jaccard well under the near-duplicate
// bar — so what these tests measure is the family cap and not the
// restatement check, which would otherwise collapse them first and make
// the cap look like it worked when it never ran.
func crowdedCorpus() []*homunculus.Instinct {
	return []*homunculus.Instinct{
		mkI("long-build", 0.85, "tail the log until the marker appears"),
		mkI("deploy-rollout", 0.85, "watch rollout status in a loop"),
		mkI("ci-conclusion", 0.85, "query the workflow API for its conclusion"),
		mkI("schema-migration", 0.85, "run the migration before deploying"),
	}
}

// oneFamily stamps the three waiting-related notes as one family and the
// unrelated note as another, the way an evolve pass would.
func oneFamily() map[string]int {
	return map[string]int{"long-build": 0, "deploy-rollout": 0, "ci-conclusion": 0, "schema-migration": 1}
}

func TestClusterCapTrimsOneFamily(t *testing.T) {
	got, ids := Build(crowdedCorpus(), nil, Options{ClusterOf: oneFamily()})
	family := 0
	for _, id := range ids {
		if id != "schema-migration" {
			family++
		}
	}
	if family != DefaultClusterCap {
		t.Errorf("family members rendered = %d, want %d\n%s", family, DefaultClusterCap, got)
	}
	// Trimming is only worth doing for what it makes room for.
	if !strings.Contains(got, "run the migration") {
		t.Errorf("the other subject was crowded out:\n%s", got)
	}
}

// TestUnstampedCorpusIsUncapped is the other direction, and the one that
// matters most: a corpus no evolve pass has stamped must be UNCAPPED
// rather than capped-at-one. Guessing that an unstamped instinct is alone
// in its family would drop instincts on information the file does not
// carry.
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
	stamp := oneFamily()
	delete(stamp, "schema-migration")
	_, ids := Build(crowdedCorpus(), nil, Options{ClusterOf: stamp})
	if len(ids) != 3 { // 2 of the family + the unstamped one
		t.Errorf("rendered = %d, want 3 (ids: %v)", len(ids), ids)
	}
	if !strings.Contains(strings.Join(ids, ","), "schema-migration") {
		t.Errorf("the unstamped instinct was dropped: %v", ids)
	}
}
