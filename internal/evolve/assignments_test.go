package evolve

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

func clusterOf(ids ...string) Cluster {
	c := Cluster{}
	for _, id := range ids {
		c.Members = append(c.Members, &homunculus.Instinct{ID: id, Trigger: "when " + id, Body: "## Action\ndo " + id})
	}
	return c
}

func TestNewClusterAssignmentsStampsEveryMember(t *testing.T) {
	ca := NewClusterAssignments([]Cluster{clusterOf("a", "b"), clusterOf("c", "d", "e")})
	if len(ca.ByInstinct) != 5 {
		t.Fatalf("stamped %d, want 5: %v", len(ca.ByInstinct), ca.ByInstinct)
	}
	if ca.ByInstinct["a"] != ca.ByInstinct["b"] {
		t.Error("members of one cluster carry different ids")
	}
	if ca.ByInstinct["a"] == ca.ByInstinct["c"] {
		t.Error("members of different clusters share an id")
	}
}

// A singleton is not a family: stamping it would hand the cap a group of
// one to enforce (indistinguishable from no cap) while still counting as
// "stamped" in the population report doctor prints.
func TestSingletonClustersAreNotStamped(t *testing.T) {
	ca := NewClusterAssignments([]Cluster{clusterOf("alone"), clusterOf("a", "b")})
	if _, ok := ca.ByInstinct["alone"]; ok {
		t.Error("a one-member cluster was stamped")
	}
	if len(ca.ByInstinct) != 2 {
		t.Errorf("stamped %d, want 2", len(ca.ByInstinct))
	}
}

func TestClusterAssignmentsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evolved", "cluster-assignments.json")
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	if err := NewClusterAssignments([]Cluster{clusterOf("a", "b")}).Save(path, now); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadClusterAssignments(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.ByInstinct) != 2 {
		t.Errorf("loaded %d stamps, want 2", len(got.ByInstinct))
	}
	if !got.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, now)
	}
	if n := got.StampedAmong([]string{"a", "b", "never-clustered"}); n != 2 {
		t.Errorf("StampedAmong = %d, want 2", n)
	}
}

// A missing file is an empty stamp, not an error: a corpus that has
// never been clustered has no families to cap, and the injector must
// treat that as uncapped rather than refusing to inject.
func TestLoadClusterAssignmentsMissingFileIsEmpty(t *testing.T) {
	ca, err := LoadClusterAssignments(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("missing file returned an error: %v", err)
	}
	if len(ca.ByInstinct) != 0 {
		t.Errorf("stamps = %d, want 0", len(ca.ByInstinct))
	}
	if n := ca.StampedAmong([]string{"a"}); n != 0 {
		t.Errorf("StampedAmong on an empty stamp = %d, want 0", n)
	}
}
