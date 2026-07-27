package evolve

import (
	"sort"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// tokenMember builds a member whose token set is exactly the given
// words, so a test can state the cohesion graph directly instead of
// reverse-engineering it from prose that happens to tokenize a certain
// way. Jaccard over these sets is what the clustering actually sees.
func tokenMember(id string, words ...string) memberToken {
	toks := map[string]struct{}{}
	for _, w := range words {
		toks[w] = struct{}{}
	}
	return memberToken{Instinct: &homunculus.Instinct{ID: id}, Tokens: toks}
}

// componentIDs renders components as sorted "a+b+c" strings, sorted, so
// an assertion reads as the grouping it claims rather than as indices.
func componentIDs(comps [][]memberToken) []string {
	out := make([]string, 0, len(comps))
	for _, comp := range comps {
		ids := make([]string, 0, len(comp))
		for _, m := range comp {
			ids = append(ids, m.Instinct.ID)
		}
		sort.Strings(ids)
		out = append(out, strings.Join(ids, "+"))
	}
	sort.Strings(out)
	return out
}

// chainMembers builds A—B—C—D: consecutive members share a token, the
// ends share nothing. This is the exact shape single linkage fuses into
// one cluster and clique percolation must break apart.
func chainMembers() []memberToken {
	return []memberToken{
		tokenMember("a", "x1", "x2"),
		tokenMember("b", "x2", "x3"),
		tokenMember("c", "x3", "x4"),
		tokenMember("d", "x4", "x5"),
	}
}

// TestCliqueK2IsSingleLinkage is the regression net for the CPM switch:
// k=2 must reproduce the previous single-linkage behaviour exactly, so
// the change is a generalisation rather than a rewrite with unknown
// blast radius. The chain A—B—C—D is the discriminating case — under
// single linkage it is ONE component.
func TestCliqueK2IsSingleLinkage(t *testing.T) {
	got := componentIDs(cliqueCommunities(chainMembers(), 0.20, 2))
	want := []string{"a+b+c+d"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("k=2 components = %v, want %v (k=2 must equal single linkage)", got, want)
	}
}

// TestCliqueK3BreaksChains is the point of the port: with k=3 a member
// must sit in a triangle, so a chain of weak bridges no longer collapses
// into one incoherent cluster. Every chain member falls out as a
// singleton, which GATE 1 then rejects explicitly.
func TestCliqueK3BreaksChains(t *testing.T) {
	got := componentIDs(cliqueCommunities(chainMembers(), 0.20, 3))
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("k=3 components = %v, want 4 singletons %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("k=3 components = %v, want %v", got, want)
			break
		}
	}
}

// TestCliqueK3KeepsTriangle pins the other half: a genuine dense group
// (every pair similar) survives k=3 as one community. A chain-breaking
// change that also destroyed real clusters would be useless.
func TestCliqueK3KeepsTriangle(t *testing.T) {
	members := []memberToken{
		tokenMember("a", "s1", "s2", "s3"),
		tokenMember("b", "s1", "s2", "s3"),
		tokenMember("c", "s1", "s2", "s3"),
	}
	got := componentIDs(cliqueCommunities(members, 0.20, 3))
	if len(got) != 1 || got[0] != "a+b+c" {
		t.Errorf("triangle components = %v, want [a+b+c]", got)
	}
}

// TestCliquePercolationMergesOverlappingTriangles pins percolation
// proper: two triangles sharing k-1 = 2 nodes are ONE community, while
// a triangle attached by a single shared node is not.
func TestCliquePercolationMergesOverlappingTriangles(t *testing.T) {
	// a,b,c mutually similar; b,c,d mutually similar (shares 2 nodes with
	// the first triangle) ⇒ percolates into one community.
	members := []memberToken{
		tokenMember("a", "p1", "p2", "p3"),
		tokenMember("b", "p1", "p2", "p3"),
		tokenMember("c", "p1", "p2", "p3"),
		tokenMember("d", "p1", "p2", "p3"),
	}
	got := componentIDs(cliqueCommunities(members, 0.20, 3))
	if len(got) != 1 || got[0] != "a+b+c+d" {
		t.Errorf("overlapping triangles = %v, want [a+b+c+d]", got)
	}
}

// TestCliqueCommunitiesPartitionsMembers pins the invariant bough's
// downstream depends on: every member appears in exactly one component.
// True CPM communities can overlap; a shared member would be emitted
// into two skills and would break Discover's min-id tiebreak, so the
// resolution to a partition is part of the contract, not an accident.
func TestCliqueCommunitiesPartitionsMembers(t *testing.T) {
	// Two triangles joined through one shared member (e) — the classic
	// overlap case: {a,b,e} and {c,d,e} each form a triangle.
	members := []memberToken{
		tokenMember("a", "g1", "g2", "g3"),
		tokenMember("b", "g1", "g2", "g3"),
		tokenMember("c", "h1", "h2", "h3"),
		tokenMember("d", "h1", "h2", "h3"),
		tokenMember("e", "g1", "g2", "g3", "h1", "h2", "h3"),
	}
	comps := cliqueCommunities(members, 0.20, 3)
	seen := map[string]int{}
	for _, comp := range comps {
		for _, m := range comp {
			seen[m.Instinct.ID]++
		}
	}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if seen[id] != 1 {
			t.Errorf("member %q appears in %d components, want exactly 1 (%v)", id, seen[id], componentIDs(comps))
		}
	}
}

// TestCommunitiesSharingTheirLowestMemberStaySeparate is the regression
// net for a partition-labelling defect: communities were labelled by
// their smallest member index, so two communities that never percolated
// but happened to share their LOWEST-indexed node (a hub) collapsed into
// one label — re-fusing exactly the chain CPM exists to break, and
// silently, since the count still looked plausible.
//
// Here the hub is member "a" at index 0: {a,b,c} and {a,d,e} are both
// triangles, share only one node (percolation needs k-1 = 2), and both
// have minIdx 0.
func TestCommunitiesSharingTheirLowestMemberStaySeparate(t *testing.T) {
	members := []memberToken{
		tokenMember("a", "g1", "g2", "g3", "h1", "h2", "h3"), // hub, lowest index
		tokenMember("b", "g1", "g2", "g3"),
		tokenMember("c", "g1", "g2", "g3"),
		tokenMember("d", "h1", "h2", "h3"),
		tokenMember("e", "h1", "h2", "h3"),
	}
	got := componentIDs(cliqueCommunities(members, 0.20, 3))
	if len(got) != 2 {
		t.Fatalf("two triangles sharing one node = %v, want 2 separate communities", got)
	}
	// The hub is assigned to exactly one of them (partition invariant);
	// the other keeps its remaining members rather than vanishing.
	if got[0] != "a+b+c" || got[1] != "d+e" {
		t.Errorf("components = %v, want [a+b+c d+e]", got)
	}
}

// TestCliqueCommunitiesEmptyAndSingleton pins the degenerate inputs the
// discovery step can legitimately hand over.
func TestCliqueCommunitiesEmptyAndSingleton(t *testing.T) {
	if got := cliqueCommunities(nil, 0.20, 3); got != nil {
		t.Errorf("empty input = %v, want nil", got)
	}
	one := []memberToken{tokenMember("solo", "z1")}
	got := componentIDs(cliqueCommunities(one, 0.20, 3))
	if len(got) != 1 || got[0] != "solo" {
		t.Errorf("single member = %v, want [solo] (singleton, for GATE 1 to reject explicitly)", got)
	}
}

// TestDefaultThresholdsCliqueK pins the shipped default so a silent
// revert to single linkage would fail here.
func TestDefaultThresholdsCliqueK(t *testing.T) {
	if k := DefaultThresholds().CliqueK; k != 3 {
		t.Errorf("DefaultThresholds().CliqueK = %d, want 3", k)
	}
}
