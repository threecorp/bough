package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/instinctgate"
)

// seedProject writes one instinct into a project's personal corpus and
// registers the project, so promoteInstincts can find it.
func seedProject(t *testing.T, layout homunculus.Layout, projectID, id, trigger, action string) {
	t.Helper()
	if err := layout.EnsureProjectDirs(projectID); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: " + id + "\ntrigger: " + trigger + "\nconfidence: 0.9\nscope: project\n---\n\n" +
		"## Action\n" + action + "\n"
	if err := os.WriteFile(filepath.Join(layout.InstinctsDir(projectID), id+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := homunculus.NewRegistryRW(layout).WriteUpsert(homunculus.Project{
		ID: projectID, Name: projectID, Root: "/tmp/" + projectID,
	}); err != nil {
		t.Fatal(err)
	}
}

// promoteFixture seeds the SAME instinct in two projects (the
// cross-project threshold) so it is a genuine promotion candidate.
func promoteFixture(t *testing.T, id, trigger, action string) homunculus.Layout {
	t.Helper()
	layout := homunculus.FromRoot(t.TempDir())
	if err := layout.EnsureGlobalDirs(); err != nil {
		t.Fatal(err)
	}
	seedProject(t, layout, "projA", id, trigger, action)
	seedProject(t, layout, "projB", id, trigger, action)
	return layout
}

// TestPromoteIsGated is the hole this closes. Promotion writes into the
// global corpus, which is injected into EVERY project — so an instinct
// recommending a forbidden action that reaches global scope is
// recommended everywhere at once. Before this, promotion ran no content
// check at all.
func TestPromoteIsGated(t *testing.T) {
	layout := promoteFixture(t, "merge-when-green",
		"when CI is green on an approved PR",
		"Run `gh pr merge --squash` to land it.")

	opt := promoteOptions{
		minProjects:   2,
		minConfidence: 0.8,
		gate:          instinctgate.New(instinctgate.Config{Enabled: true}),
	}
	res, err := promoteInstincts(layout, opt, time.Now())
	if err != nil {
		t.Fatalf("promoteInstincts: %v", err)
	}
	if len(res.promoted) != 0 {
		t.Errorf("a forbidden action reached global scope: %+v", res.promoted)
	}
	if len(res.gateHeld) != 1 || res.gateHeld[0].rule != "never-merge-unasked" {
		t.Fatalf("gateHeld = %+v, want one never-merge-unasked hold", res.gateHeld)
	}
	// Nothing was written to the global corpus.
	global, _ := homunculus.ScanInstincts(layout.GlobalInstinctsDir())
	if len(global) != 0 {
		t.Errorf("global corpus = %+v, want empty", global)
	}
}

// TestPromoteClearsBenignInstinct pins the other half: the gate must not
// block ordinary knowledge from reaching global scope, or promotion
// stops working entirely.
func TestPromoteClearsBenignInstinct(t *testing.T) {
	layout := promoteFixture(t, "read-before-edit",
		"when editing unfamiliar files",
		"Read the surrounding implementation before editing.")

	opt := promoteOptions{
		minProjects:   2,
		minConfidence: 0.8,
		gate:          instinctgate.New(instinctgate.Config{Enabled: true}),
	}
	res, err := promoteInstincts(layout, opt, time.Now())
	if err != nil {
		t.Fatalf("promoteInstincts: %v", err)
	}
	if len(res.gateHeld) != 0 {
		t.Fatalf("benign instinct was held: %+v", res.gateHeld)
	}
	if len(res.promoted) != 1 {
		t.Fatalf("promoted = %+v, want the benign instinct", res.promoted)
	}
	global, _ := homunculus.ScanInstincts(layout.GlobalInstinctsDir())
	if len(global) != 1 || global[0].ID != "read-before-edit" {
		t.Errorf("global corpus = %+v, want read-before-edit", global)
	}
}

// TestPromoteGateHoldIsReported pins that a withheld promotion is named
// with its rule. A silent refusal at the widest blast radius in the
// system is the last place to hide a decision — the operator would see
// "promoted 0" and have no way to tell it apart from "nothing qualified".
func TestPromoteGateHoldIsReported(t *testing.T) {
	res := promoteResult{
		gateHeld: []gateHold{{id: "merge-when-green", rule: "never-merge-unasked"}},
		dryRun:   true,
	}
	var b strings.Builder
	renderPromote(&b, res)
	out := b.String()
	for _, want := range []string{"withheld", "merge-when-green", "never-merge-unasked"} {
		if !strings.Contains(out, want) {
			t.Errorf("promote output missing %q:\n%s", want, out)
		}
	}
}
