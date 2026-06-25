package evolve

import (
	"sort"
	"strings"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// memberToken is one instinct projected into the clustering space.
// Tokens caches Tokenize(id + trigger + action) so the O(N²) cohesion
// sweep does not re-tokenize on every pair.
type memberToken struct {
	Instinct *homunculus.Instinct
	Tokens   map[string]struct{}
}

// Prior is one existing cluster label + description, the catalog
// against which GATE 3' / GATE 4 measure a candidate. Tokens caches
// Tokenize(label + description).
type Prior struct {
	Label       string
	Description string
	Tokens      map[string]struct{}
}

// Cluster is a connected component of weakly-attached instincts that
// survived discovery. The mechanical gates score it; the LLM judge
// labels it.
type Cluster struct {
	Members          []*homunculus.Instinct
	memberTokens     []map[string]struct{}
	candidateTokens  map[string]struct{}
	NearestPrior     *Prior
	NearestOverlap   float64
}

// instinctSurface concatenates the fields ECC tokenizes for
// clustering: id + trigger + the first action line + the body. The
// body is included (not just the action line) because bough's
// instinct files carry the full ## Action / ## Evidence prose and
// the extra surface area sharpens cohesion.
func instinctSurface(in *homunculus.Instinct) string {
	var b strings.Builder
	b.WriteString(in.ID)
	b.WriteByte(' ')
	b.WriteString(in.Trigger)
	b.WriteByte(' ')
	b.WriteString(in.Body)
	return b.String()
}

// Discover groups instincts into candidate clusters per the ECC v3
// discovery algorithm:
//
//  1. Tokenize every instinct.
//  2. A "weakly attached" instinct = one whose maximum overlap with
//     any prior is below the lexicon-coverage threshold (= seeds the
//     candidate space; strongly-attached instincts already belong to
//     an existing cluster).
//  3. Build a graph over the weak instincts: edge i—j exists when
//     Jaccard(i, j) >= COH_MIN.
//  4. Connected components of that graph are the candidate clusters.
//
// priors may be nil (= first evolve pass, every instinct is weakly
// attached). The returned clusters carry their nearest-prior link so
// the downstream gates do not recompute it.
func Discover(instincts []*homunculus.Instinct, priors []Prior, th Thresholds) []Cluster {
	members := make([]memberToken, 0, len(instincts))
	for _, in := range instincts {
		members = append(members, memberToken{
			Instinct: in,
			Tokens:   Tokenize(instinctSurface(in)),
		})
	}

	// Step 2: weak-attachment filter.
	weak := make([]memberToken, 0, len(members))
	for _, m := range members {
		if maxPriorOverlap(m.Tokens, priors) < th.LexiconCoverageMax {
			weak = append(weak, m)
		}
	}

	// Step 3 + 4: connected components over the cohesion graph.
	components := connectedComponents(weak, th.CohesionMin)

	clusters := make([]Cluster, 0, len(components))
	for _, comp := range components {
		c := buildCluster(comp, priors)
		clusters = append(clusters, c)
	}

	// Largest clusters first so preview output + audit surface the
	// most-evidenced candidates at the top.
	sort.SliceStable(clusters, func(i, j int) bool {
		return len(clusters[i].Members) > len(clusters[j].Members)
	})
	return clusters
}

func buildCluster(comp []memberToken, priors []Prior) Cluster {
	c := Cluster{
		Members:      make([]*homunculus.Instinct, 0, len(comp)),
		memberTokens: make([]map[string]struct{}, 0, len(comp)),
	}
	sets := make([]map[string]struct{}, 0, len(comp))
	for _, m := range comp {
		c.Members = append(c.Members, m.Instinct)
		c.memberTokens = append(c.memberTokens, m.Tokens)
		sets = append(sets, m.Tokens)
	}
	c.candidateTokens = union(sets...)
	c.NearestPrior, c.NearestOverlap = nearestPrior(c.candidateTokens, priors)
	return c
}

// connectedComponents builds the cohesion graph (edge when pairwise
// Jaccard >= cohMin) and returns its connected components via union-
// find. Singletons (= no qualifying edge) come back as one-member
// components so GATE 1 can reject them explicitly rather than
// dropping them silently.
func connectedComponents(members []memberToken, cohMin float64) [][]memberToken {
	n := len(members)
	if n == 0 {
		return nil
	}
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union2 := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if Jaccard(members[i].Tokens, members[j].Tokens) >= cohMin {
				union2(i, j)
			}
		}
	}
	groups := map[int][]memberToken{}
	for i := 0; i < n; i++ {
		root := find(i)
		groups[root] = append(groups[root], members[i])
	}
	out := make([][]memberToken, 0, len(groups))
	for _, g := range groups {
		out = append(out, g)
	}
	return out
}

func maxPriorOverlap(tokens map[string]struct{}, priors []Prior) float64 {
	best := 0.0
	for _, p := range priors {
		if s := Jaccard(tokens, priorTokens(&p)); s > best {
			best = s
		}
	}
	return best
}

func nearestPrior(tokens map[string]struct{}, priors []Prior) (*Prior, float64) {
	best := 0.0
	var nearest *Prior
	for i := range priors {
		s := Jaccard(tokens, priorTokens(&priors[i]))
		if s > best {
			best = s
			nearest = &priors[i]
		}
	}
	return nearest, best
}

// priorTokens lazily tokenizes a prior's (label + description) the
// first time it is needed and caches the result on the Prior.
func priorTokens(p *Prior) map[string]struct{} {
	if p.Tokens == nil {
		p.Tokens = Tokenize(p.Label + " " + p.Description)
	}
	return p.Tokens
}
