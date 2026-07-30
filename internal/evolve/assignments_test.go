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

func TestArrivalBacklogCountsSinceTheLastPass(t *testing.T) {
	pass := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	older := &homunculus.Instinct{ID: "older", FirstSeen: pass.Add(-48 * time.Hour)}
	newer := &homunculus.Instinct{ID: "newer", FirstSeen: pass.Add(48 * time.Hour)}
	ca := &ClusterAssignments{ByInstinct: map[string]int{"older": 0}, UpdatedAt: pass}

	b := ArrivalBacklog{Threshold: 1}
	if n, overdue := b.Count([]*homunculus.Instinct{older, newer}, ca, nil); n != 1 || !overdue {
		t.Errorf("Count = (%d, %v), want (1, true) — only the arrival after the pass counts", n, overdue)
	}
	if n, overdue := b.Count([]*homunculus.Instinct{older}, ca, nil); n != 0 || overdue {
		t.Errorf("Count = (%d, %v), want (0, false)", n, overdue)
	}
	// Never clustered: nothing has been routed, so everything is a backlog.
	if n, _ := b.Count([]*homunculus.Instinct{older, newer}, nil, nil); n != 2 {
		t.Errorf("unstamped Count = %d, want 2", n)
	}
	// Under the threshold says nothing at all.
	if _, overdue := (ArrivalBacklog{Threshold: 5}).Count([]*homunculus.Instinct{older, newer}, nil, nil); overdue {
		t.Error("a corpus under the threshold reported overdue")
	}
	if got := DefaultArrivalBacklog().Threshold; got != 60 {
		t.Errorf("default threshold = %d, want 60", got)
	}
}

// A note with no dates at all (imported, or hand-written) must not be
// counted as having arrived since a pass that happened after it: falling
// back to LastSeen is what keeps such a corpus from re-announcing forever.
func TestArrivalBacklogUndatedNoteDoesNotResurrectTheNotice(t *testing.T) {
	ca := &ClusterAssignments{UpdatedAt: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)}
	undated := &homunculus.Instinct{ID: "undated"}
	if n, _ := (ArrivalBacklog{Threshold: 1}).Count([]*homunculus.Instinct{undated}, ca, nil); n != 0 {
		t.Errorf("an undated note counted as a new arrival (n=%d)", n)
	}
}

// TestArrivalBacklogSkipsWhatIsAlreadyRouted is the other half of "a notice
// that cannot be cleared is a notice that gets ignored". A note a deployed
// skill delivers, or one the operator silenced in their register, HAS been
// routed — counting it leaves the number stuck whatever the operator does
// about individual notes.
func TestArrivalBacklogSkipsWhatIsAlreadyRouted(t *testing.T) {
	fresh := func(id string) *homunculus.Instinct {
		return &homunculus.Instinct{ID: id, FirstSeen: time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)}
	}
	corpus := []*homunculus.Instinct{fresh("a"), fresh("b"), fresh("c")}
	b := ArrivalBacklog{Threshold: 3}

	if n, overdue := b.Count(corpus, nil, nil); n != 3 || !overdue {
		t.Errorf("Count = (%d, %v), want (3, true) with nothing excluded", n, overdue)
	}
	if n, overdue := b.Count(corpus, nil, map[string]struct{}{"a": {}, "b": {}}); n != 1 || overdue {
		t.Errorf("Count = (%d, %v), want (1, false) — routed notes must not count", n, overdue)
	}
}
