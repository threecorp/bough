package evolve

import "sort"

// Clique percolation replaces the single-linkage components bough used
// through v0.19. Single linkage merges on ONE qualifying edge, so a
// chain A—B—C—D collapses into one cluster even when A and D share
// nothing: the resulting "skill" is a bag of unrelated notes that reads
// as incoherent and drags an LLM judge toward DOUBT.
//
// CPM (Palla et al. 2005) instead percolates k-cliques: a community is
// a maximal chain of k-cliques where consecutive cliques share k-1
// nodes. Every member therefore sits in a k-clique — a chain link alone
// is not enough — so weak bridges no longer fuse dense groups.
//
// k is a threshold (Thresholds.CliqueK), not a constant, because it is
// the precision/recall dial AND the migration story: k=2 makes 2-cliques
// (= edges) percolate through shared endpoints, which is EXACTLY
// single-linkage connected components. So k=2 reproduces the previous
// behaviour bit-for-bit and k=3 (the default) is the chain-breaking
// choice. A test pins that equivalence as the regression net.
//
// Note the floor this implies: with k=3 no community can have fewer
// than 3 members, so a k above Thresholds.MemberMin raises the
// effective member floor. That is deliberate but must never be silent —
// `bough evolve` prints CliqueK alongside the other thresholds.

// cliqueCommunities groups members into k-clique-percolation
// communities over the cohesion graph (edge when pairwise Jaccard >=
// cohMin). Members in no k-clique are returned as one-member components
// so GATE 1 rejects them explicitly rather than the discovery step
// dropping them silently.
//
// CPM communities may genuinely overlap (a node can bridge two
// communities). bough's downstream assumes clusters PARTITION the
// corpus — Discover's tiebreak keys on each cluster's min member id, and
// a shared member would be emitted into two skills — so overlaps are
// resolved to a partition here: a shared node stays with the largest
// community, ties broken by the community's smallest member index. The
// overlap information is not currently consumed by any caller, so it is
// resolved rather than plumbed through.
func cliqueCommunities(members []memberToken, cohMin float64, k int) [][]memberToken {
	n := len(members)
	if n == 0 {
		return nil
	}
	if k < 2 {
		k = 2
	}
	adj := buildAdjacency(members, cohMin)
	cliques := enumerateCliques(adj, k)

	// Percolate: two k-cliques are adjacent when they share k-1 nodes.
	// Union-find over cliques, then a community is the union of the
	// nodes of its cliques.
	parent := make([]int, len(cliques))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int //nolint:staticcheck // recursive closure: declaration must precede assignment
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for i := 0; i < len(cliques); i++ {
		for j := i + 1; j < len(cliques); j++ {
			if sharedNodes(cliques[i], cliques[j]) == k-1 {
				ri, rj := find(i), find(j)
				if ri != rj {
					parent[ri] = rj
				}
			}
		}
	}
	communityNodes := map[int]map[int]struct{}{}
	for i, cl := range cliques {
		root := find(i)
		if communityNodes[root] == nil {
			communityNodes[root] = map[int]struct{}{}
		}
		for _, node := range cl {
			communityNodes[root][node] = struct{}{}
		}
	}

	assigned := resolveToPartition(communityNodes)

	// Emit communities (deterministically ordered by their smallest
	// member index), then every unassigned node as a singleton.
	grouped := map[int][]int{}
	for node, owner := range assigned {
		grouped[owner] = append(grouped[owner], node)
	}
	owners := make([]int, 0, len(grouped))
	for owner := range grouped {
		owners = append(owners, owner)
	}
	sort.Ints(owners)

	out := make([][]memberToken, 0, len(owners)+n)
	for _, owner := range owners {
		nodes := grouped[owner]
		sort.Ints(nodes)
		comp := make([]memberToken, 0, len(nodes))
		for _, idx := range nodes {
			comp = append(comp, members[idx])
		}
		out = append(out, comp)
	}
	for i := 0; i < n; i++ {
		if _, ok := assigned[i]; !ok {
			out = append(out, []memberToken{members[i]})
		}
	}
	return out
}

// buildAdjacency returns the sorted neighbour list per member for the
// cohesion graph. Sorted so clique enumeration can extend by increasing
// index and never emit the same clique twice.
func buildAdjacency(members []memberToken, cohMin float64) [][]int {
	n := len(members)
	adj := make([][]int, n)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if Jaccard(members[i].Tokens, members[j].Tokens) >= cohMin {
				adj[i] = append(adj[i], j)
				adj[j] = append(adj[j], i)
			}
		}
	}
	for i := range adj {
		sort.Ints(adj[i])
	}
	return adj
}

// enumerateCliques lists every k-clique as a sorted node slice. It
// extends each partial clique only with neighbours of HIGHER index that
// are adjacent to all current members, so each clique is generated
// exactly once and the search prunes early on sparse graphs (the
// realistic shape here — cohesion edges are the exception, not the
// rule).
func enumerateCliques(adj [][]int, k int) [][]int {
	var out [][]int
	var extend func(current []int, candidates []int)
	extend = func(current, candidates []int) {
		if len(current) == k {
			clique := make([]int, len(current))
			copy(clique, current)
			out = append(out, clique)
			return
		}
		for i, cand := range candidates {
			// Only candidates adjacent to every current member survive;
			// restricting to those after i keeps generation unique.
			next := intersectSorted(candidates[i+1:], adj[cand])
			extend(append(current, cand), next)
		}
	}
	all := make([]int, len(adj))
	for i := range all {
		all[i] = i
	}
	extend(nil, all)
	return out
}

// intersectSorted returns the sorted intersection of two sorted slices.
func intersectSorted(a, b []int) []int {
	out := make([]int, 0, min(len(a), len(b)))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, a[i])
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return out
}

// sharedNodes counts the nodes two sorted cliques have in common.
func sharedNodes(a, b []int) int {
	return len(intersectSorted(a, b))
}

// resolveToPartition maps each node to exactly one community root.
// A node in several overlapping communities goes to the largest; ties
// break on the community's smallest member index so the outcome does
// not depend on map iteration order.
func resolveToPartition(communityNodes map[int]map[int]struct{}) map[int]int {
	type community struct {
		root   int
		size   int
		minIdx int
		nodes  map[int]struct{}
	}
	comms := make([]community, 0, len(communityNodes))
	for root, nodes := range communityNodes {
		minIdx := -1
		for node := range nodes {
			if minIdx == -1 || node < minIdx {
				minIdx = node
			}
		}
		comms = append(comms, community{root: root, size: len(nodes), minIdx: minIdx, nodes: nodes})
	}
	sort.Slice(comms, func(i, j int) bool {
		if comms[i].size != comms[j].size {
			return comms[i].size > comms[j].size
		}
		return comms[i].minIdx < comms[j].minIdx
	})
	assigned := map[int]int{}
	for _, c := range comms {
		for node := range c.nodes {
			if _, taken := assigned[node]; !taken {
				assigned[node] = c.minIdx
			}
		}
	}
	return assigned
}
