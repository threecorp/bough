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
	Members         []*homunculus.Instinct
	memberTokens    []map[string]struct{}
	candidateTokens map[string]struct{}
	NearestPrior    *Prior
	NearestOverlap  float64
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
//  4. Clique-percolation communities of that graph (Thresholds.CliqueK,
//     see cpm.go) are the candidate clusters. Single linkage — the
//     pre-CPM behaviour, still reachable with CliqueK=2 — merged on one
//     qualifying edge, so a chain A—B—C—D became one incoherent cluster
//     even when A and D shared nothing.
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

	// Step 3 + 4: clique-percolation communities over the cohesion graph.
	components := cliqueCommunities(weak, th.CohesionMin, th.CliqueK)

	clusters := make([]Cluster, 0, len(components))
	for _, comp := range components {
		c := buildCluster(comp, priors)
		clusters = append(clusters, c)
	}

	// Largest clusters first so preview output + audit surface the
	// most-evidenced candidates at the top. Tiebreak by Members[0].ID (each
	// cluster's min id; clusters partition the corpus → keys are unique) so
	// the order is TOTAL and deterministic — otherwise equal-size clusters
	// kept the random map-iteration order, and under the GATE-5 call cap
	// which clusters got judged (vs defaulted to DOUBT) changed per run.
	sort.SliceStable(clusters, func(i, j int) bool {
		if li, lj := len(clusters[i].Members), len(clusters[j].Members); li != lj {
			return li > lj
		}
		return clusters[i].Members[0].ID < clusters[j].Members[0].ID
	})
	return clusters
}

func buildCluster(comp []memberToken, priors []Prior) Cluster {
	// Order members by id so Members[0] is the cluster's min-id member
	// regardless of caller input order (connectedComponents builds
	// components by ranging a map, which Go randomizes per run). This makes
	// the cluster's identity — and the Discover tiebreak — deterministic.
	// comp is a fresh per-component slice with no other reader, so an
	// in-place sort is safe.
	sort.SliceStable(comp, func(i, j int) bool {
		return comp[i].Instinct.ID < comp[j].Instinct.ID
	})
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
