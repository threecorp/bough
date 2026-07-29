package cli

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/evolve"
	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// TestQuietPassDoesNotWipeTheClusterStamp pins the failure mode the
// per-family cap would otherwise walk into: a pass over an EMPTY corpus
// takes the early path and hands in a zero Outcome, and writing an empty
// stamp there would leave the cap guarded on a field nothing had
// written — inert, while still reading as implemented. The previous
// stamp must survive a pass that never clustered.
func TestQuietPassDoesNotWipeTheClusterStamp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BOUGH_HOMUNCULUS_DIR", home)
	layout := homunculus.NewLayout()
	ident := homunculus.ProjectIdentity{ID: "deadbeef", Name: "fixture", Root: t.TempDir()}
	stampPath := layout.ClusterAssignmentsFile(ident.ID)

	// An earlier pass stamped two families.
	prev := &evolve.ClusterAssignments{ByInstinct: map[string]int{"a": 0, "b": 0, "c": 1, "d": 1}}
	if err := prev.Save(stampPath, time.Now()); err != nil {
		t.Fatalf("seed the stamp: %v", err)
	}

	var out, errOut bytes.Buffer
	labels := &evolve.ClusterLabels{Labels: map[string]string{}}
	labelsPath := filepath.Join(home, "cluster-labels.json")
	// The empty-corpus path: a zero Outcome, so Assignments is nil.
	if err := persistEvolveOutcome(&out, &errOut, ident, layout, labels, labelsPath,
		evolve.Outcome{}, evolve.DefaultThresholds(), true, nil); err != nil {
		t.Fatalf("persist: %v", err)
	}

	got, err := evolve.LoadClusterAssignments(stampPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got.ByInstinct) != 4 {
		t.Errorf("stamps after a quiet pass = %d, want the previous 4 (%v)", len(got.ByInstinct), got.ByInstinct)
	}
}

// The other direction: a pass that DID cluster replaces the stamp
// wholesale, so a family that no longer exists stops being capped.
func TestClusteringPassReplacesTheStamp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BOUGH_HOMUNCULUS_DIR", home)
	layout := homunculus.NewLayout()
	ident := homunculus.ProjectIdentity{ID: "deadbeef", Name: "fixture", Root: t.TempDir()}
	stampPath := layout.ClusterAssignmentsFile(ident.ID)
	if err := (&evolve.ClusterAssignments{ByInstinct: map[string]int{"gone-a": 0, "gone-b": 0}}).Save(stampPath, time.Now()); err != nil {
		t.Fatalf("seed the stamp: %v", err)
	}

	var out, errOut bytes.Buffer
	labels := &evolve.ClusterLabels{Labels: map[string]string{}}
	outcome := evolve.Outcome{Assignments: &evolve.ClusterAssignments{ByInstinct: map[string]int{"fresh-a": 0, "fresh-b": 0}}}
	if err := persistEvolveOutcome(&out, &errOut, ident, layout, labels,
		filepath.Join(home, "cluster-labels.json"), outcome, evolve.DefaultThresholds(), true, nil); err != nil {
		t.Fatalf("persist: %v", err)
	}

	got, err := evolve.LoadClusterAssignments(stampPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, stale := got.ByInstinct["gone-a"]; stale {
		t.Error("a family from the previous pass survived a clustering pass")
	}
	if _, fresh := got.ByInstinct["fresh-a"]; !fresh {
		t.Errorf("this pass's stamp did not land: %v", got.ByInstinct)
	}
}
