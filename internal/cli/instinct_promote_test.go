package cli

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// approxConf compares confidence values with a tolerance — the
// cross-project mean keeps the raw float (ECC parity), so e.g.
// (0.80+0.90)/2 lands at 0.8500000000000001.
func approxConf(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

func writeProjInstinct(t *testing.T, layout homunculus.Layout, pid, id string, conf float64) {
	t.Helper()
	in := &homunculus.Instinct{
		ID:         id,
		Trigger:    "when " + id,
		Confidence: conf,
		Domain:     "workflow",
		Scope:      "project",
		Observed:   3,
		Body:       "## Action\nDo " + id,
	}
	if _, err := homunculus.WriteInstinctFile(layout.InstinctsDir(pid), in); err != nil {
		t.Fatalf("write instinct %s/%s: %v", pid, id, err)
	}
}

func registerProjects(t *testing.T, layout homunculus.Layout, ids ...string) {
	t.Helper()
	reg := homunculus.NewRegistryRW(layout)
	for _, id := range ids {
		if err := reg.WriteUpsert(homunculus.Project{ID: id, Name: id, Root: "/tmp/" + id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
}

// TestPromoteInstincts_CrossProject is the #5 happy path: an id present
// in 2+ projects with mean confidence >= 0.8 is copied to the global
// corpus with scope:global + provenance, while single-project ids and
// low-mean ids are left alone and the source files are untouched.
func TestPromoteInstincts_CrossProject(t *testing.T) {
	layout := homunculus.FromRoot(t.TempDir())
	registerProjects(t, layout, "p1", "p2", "p3")

	// shared, high mean confidence across p1+p2 → promote
	writeProjInstinct(t, layout, "p1", "verify-before-edit", 0.85)
	writeProjInstinct(t, layout, "p2", "verify-before-edit", 0.85)
	// shared but mean (0.65) below 0.8 → below threshold, no promote
	writeProjInstinct(t, layout, "p1", "low-conf-shared", 0.60)
	writeProjInstinct(t, layout, "p2", "low-conf-shared", 0.70)
	// single project only → not general, no promote
	writeProjInstinct(t, layout, "p3", "only-p3", 0.95)

	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	res, err := promoteInstincts(layout, promoteOptions{minProjects: 2, minConfidence: 0.8}, now)
	if err != nil {
		t.Fatalf("promoteInstincts: %v", err)
	}
	if len(res.promoted) != 1 || res.promoted[0].id != "verify-before-edit" {
		t.Fatalf("promoted = %+v, want exactly [verify-before-edit]", res.promoted)
	}
	if res.belowThresh != 1 {
		t.Errorf("belowThresh = %d, want 1 (low-conf-shared)", res.belowThresh)
	}

	gpath := filepath.Join(layout.GlobalInstinctsDir(), "verify-before-edit.md")
	gi, err := homunculus.ReadInstinctFile(gpath)
	if err != nil {
		t.Fatalf("read promoted global: %v", err)
	}
	if gi.Scope != "global" {
		t.Errorf("scope = %q, want global", gi.Scope)
	}
	if !approxConf(gi.Confidence, 0.85) {
		t.Errorf("confidence = %v, want 0.85 (cross-project mean)", gi.Confidence)
	}
	if gi.Raw["source"] != "auto-promoted" {
		t.Errorf("provenance source = %v, want auto-promoted", gi.Raw["source"])
	}
	if gi.Raw["seen_in_projects"] != 2 {
		t.Errorf("seen_in_projects = %v, want 2", gi.Raw["seen_in_projects"])
	}

	// source project instincts must be untouched (ECC parity)
	if _, err := homunculus.ReadInstinctFile(filepath.Join(layout.InstinctsDir("p1"), "verify-before-edit.md")); err != nil {
		t.Errorf("source instinct disturbed in p1: %v", err)
	}
}

// TestPromoteInstincts_DryRunAndIdempotent covers --dry-run (candidate
// reported, no file written) and idempotency (a second real pass skips
// the already-global id rather than re-promoting it).
func TestPromoteInstincts_DryRunAndIdempotent(t *testing.T) {
	layout := homunculus.FromRoot(t.TempDir())
	registerProjects(t, layout, "p1", "p2")
	writeProjInstinct(t, layout, "p1", "shared-id", 0.90)
	writeProjInstinct(t, layout, "p2", "shared-id", 0.90)
	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	gpath := filepath.Join(layout.GlobalInstinctsDir(), "shared-id.md")

	// dry-run reports the candidate but writes nothing
	res, err := promoteInstincts(layout, promoteOptions{minProjects: 2, minConfidence: 0.8, dryRun: true}, now)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(res.promoted) != 1 {
		t.Fatalf("dry-run promoted = %d, want 1 candidate reported", len(res.promoted))
	}
	if _, err := os.Stat(gpath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote a file (stat err=%v)", err)
	}

	// real pass writes it
	if _, err := promoteInstincts(layout, promoteOptions{minProjects: 2, minConfidence: 0.8}, now); err != nil {
		t.Fatalf("real pass: %v", err)
	}
	if _, err := os.Stat(gpath); err != nil {
		t.Fatalf("real pass did not write global file: %v", err)
	}

	// second real pass is idempotent: already-global → skipped
	res2, err := promoteInstincts(layout, promoteOptions{minProjects: 2, minConfidence: 0.8}, now)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(res2.promoted) != 0 || len(res2.skippedGlobal) != 1 {
		t.Errorf("second pass not idempotent: promoted=%d skippedGlobal=%d", len(res2.promoted), len(res2.skippedGlobal))
	}
}

// TestPromoteInstincts_ConflictPicksHighestConfidence verifies that when
// the same id has divergent bodies across projects, the highest-confidence
// copy supplies the body/trigger while confidence is still the mean.
func TestPromoteInstincts_ConflictPicksHighestConfidence(t *testing.T) {
	layout := homunculus.FromRoot(t.TempDir())
	registerProjects(t, layout, "p1", "p2")

	lo := &homunculus.Instinct{ID: "dup", Trigger: "low version", Confidence: 0.80, Domain: "workflow", Scope: "project", Body: "## Action\nLOW"}
	hi := &homunculus.Instinct{ID: "dup", Trigger: "high version", Confidence: 0.90, Domain: "workflow", Scope: "project", Body: "## Action\nHIGH"}
	if _, err := homunculus.WriteInstinctFile(layout.InstinctsDir("p1"), lo); err != nil {
		t.Fatal(err)
	}
	if _, err := homunculus.WriteInstinctFile(layout.InstinctsDir("p2"), hi); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	if _, err := promoteInstincts(layout, promoteOptions{minProjects: 2, minConfidence: 0.8}, now); err != nil {
		t.Fatalf("promote: %v", err)
	}
	gi, err := homunculus.ReadInstinctFile(filepath.Join(layout.GlobalInstinctsDir(), "dup.md"))
	if err != nil {
		t.Fatalf("read promoted: %v", err)
	}
	if gi.Trigger != "high version" {
		t.Errorf("trigger = %q, want high version (highest-confidence source)", gi.Trigger)
	}
	if !approxConf(gi.Confidence, 0.85) {
		t.Errorf("confidence = %v, want 0.85 (mean of 0.80,0.90)", gi.Confidence)
	}
}
