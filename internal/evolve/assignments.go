package evolve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// A per-cluster cap exists so one family of restatements cannot take the
// whole prompt budget: five notes that all say "poll the background task
// output" should contribute two lines, not five, and the room they give
// back goes to a subject the prompt also touched.
//
// The cap needs to know which family each instinct belongs to, and
// clustering is O(N²) over the corpus — far too slow to run on the
// prompt hot path. So the offline pass that already clusters (evolve)
// STAMPS the membership here, and the injector reads it.
//
// The reason this file exists at all, rather than the cap keying off
// something the injector could compute: a mechanism guarded on a field
// nothing ever writes is inert for its whole life while reading as
// implemented. Upstream measured exactly that — a cap guarded on "is
// this row's cluster id set?" against a corpus where every row's id was
// null, for the mechanism's entire existence. Whoever adds a guard on a
// stored field owes an answer to "who writes it, and has that writer
// ever run"; the answer here is `bough evolve --generate`, and `bough
// doctor` prints the population count so an unstamped corpus is loud
// rather than silently uncapped.

// ClusterAssignments maps instinct id → the cluster it was discovered
// in, as of the last evolve pass.
type ClusterAssignments struct {
	// ByInstinct is keyed by instinct id, valued by a cluster INDEX
	// within the pass that wrote the file. The index is not stable
	// across passes and does not need to be: the file is rewritten
	// wholesale each pass, and the cap only ever asks "were these two
	// candidates in the same family", never "which family was it".
	//
	// A stale stamp is therefore harmless in the direction that matters
	// — the cap only ever REMOVES candidates, and the near-duplicate
	// check runs independently of it.
	ByInstinct map[string]int `json:"by_instinct"`
	// UpdatedAt is when the pass that wrote this file ran. It doubles as
	// the "last clustering pass" mark the arrival backlog counts from:
	// routing is the one manual step left in the loop, so it is the one
	// that can silently stop happening.
	UpdatedAt time.Time `json:"updated_at"`
}

// LoadClusterAssignments reads the stamp file. A missing file is an
// empty stamp — a corpus that has never been clustered has no families
// to cap, which is different from "the cap is off" and is reported as
// such by doctor.
func LoadClusterAssignments(path string) (*ClusterAssignments, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ClusterAssignments{ByInstinct: map[string]int{}}, nil
		}
		return nil, fmt.Errorf("evolve: read cluster assignments %s: %w", path, err)
	}
	ca := &ClusterAssignments{}
	if err := json.Unmarshal(raw, ca); err != nil {
		return nil, fmt.Errorf("evolve: parse cluster assignments %s: %w", path, err)
	}
	if ca.ByInstinct == nil {
		ca.ByInstinct = map[string]int{}
	}
	return ca, nil
}

// NewClusterAssignments stamps every member of every discovered cluster.
//
// Clusters below two members are skipped: a singleton is not a family,
// and stamping it would give the cap a group of one to enforce against
// — which is indistinguishable from no cap while still counting as
// "stamped" in the population report.
func NewClusterAssignments(clusters []Cluster) *ClusterAssignments {
	byInstinct := make(map[string]int, len(clusters)*2)
	for i, c := range clusters {
		if len(c.Members) < 2 {
			continue
		}
		for _, m := range c.Members {
			byInstinct[m.ID] = i
		}
	}
	return &ClusterAssignments{ByInstinct: byInstinct}
}

// StampedAmong counts how many of the given ids carry a stamp. It is
// what makes the cap's reach observable: "2 of 1371" and "1300 of 1371"
// are the difference between an inert mechanism and a working one, and
// neither is visible from the code alone.
func (c *ClusterAssignments) StampedAmong(ids []string) int {
	if c == nil || len(c.ByInstinct) == 0 {
		return 0
	}
	n := 0
	for _, id := range ids {
		if _, ok := c.ByInstinct[id]; ok {
			n++
		}
	}
	return n
}

// Save persists the stamp atomically.
func (c *ClusterAssignments) Save(path string, now time.Time) error {
	c.UpdatedAt = now.UTC()
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("evolve: marshal cluster assignments: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("evolve: mkdir %s: %w", filepath.Dir(path), err)
	}
	return atomicWriteFile(path, append(raw, '\n'))
}
