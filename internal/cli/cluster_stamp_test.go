package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/evolve"
	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/inject"
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

// TestCorruptStampLeavesSelectionUncapped pins the safe direction of the
// one failure the injector cannot report from the prompt: an unreadable
// stamp means "we do not know which family anything is in", and the cap
// must then trim NOTHING. Treating it as "every instinct is alone" would
// be a guess; treating it as one family would silently drop instincts.
//
// Asserted as an EQUIVALENCE against the no-stamp run rather than a line
// count, so the test measures the stamp's effect and nothing else — the
// floor and the restatement skip also shape this block, and pinning a
// number would make this test fail for their reasons instead of its own.
func TestCorruptStampLeavesSelectionUncapped(t *testing.T) {
	const prompt = "a build, a deploy and CI are all slow — what do I watch"
	render := func(t *testing.T, corruptStamp bool) string {
		t.Helper()
		repo := injectFixture(t, "0.9")
		ident, err := homunculus.DetectIdentity(repo)
		if err != nil {
			t.Skipf("identity resolution needs a git repo: %v", err)
		}
		layout := homunculus.NewLayout()
		for _, n := range []struct{ id, trigger, action string }{
			{"long-build", "when a build takes minutes", "tail the log until the marker appears"},
			{"deploy-rollout", "when a deploy is slow", "watch rollout status in a loop"},
			{"ci-conclusion", "when CI drags on", "query the workflow API for its conclusion"},
		} {
			body := "---\nid: " + n.id + "\ntrigger: " + n.trigger + "\nconfidence: 0.9\nscope: project\n---\n\n## Action\n" + n.action + "\n"
			if err := os.WriteFile(filepath.Join(layout.InstinctsDir(ident.ID), n.id+".md"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if corruptStamp {
			stampPath := layout.ClusterAssignmentsFile(ident.ID)
			if err := os.MkdirAll(filepath.Dir(stampPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stampPath, []byte("{not json at all"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		var buf bytes.Buffer
		if err := runInjectContext(&cobra.Command{}, &buf, repo, inject.Options{Prompt: prompt}); err != nil {
			t.Fatalf("runInjectContext: %v", err)
		}
		return buf.String()
	}

	clean := render(t, false)
	corrupt := render(t, true)
	if clean == "" {
		t.Fatal("precondition: the corpus should deliver something for this prompt")
	}
	if corrupt != clean {
		t.Errorf("an unreadable stamp changed the selection; it must cap nothing.\n--- with no stamp ---\n%s\n--- with a corrupt stamp ---\n%s", clean, corrupt)
	}
}
