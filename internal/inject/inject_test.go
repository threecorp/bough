package inject

import (
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

func mkI(id string, conf float64, action string) *homunculus.Instinct {
	return &homunculus.Instinct{
		ID:         id,
		Trigger:    "when " + id,
		Confidence: conf,
		Body:       "## Action\n" + action,
	}
}

func TestBuild_ConfidenceSort(t *testing.T) {
	project := []*homunculus.Instinct{
		mkI("low", 0.55, "do low"),
		mkI("high", 0.90, "do high"),
		mkI("mid", 0.70, "do mid"),
	}
	block, ids := Build(project, nil, Options{})
	if len(ids) != 3 {
		t.Fatalf("included = %d, want 3", len(ids))
	}
	// high must appear before mid before low
	hi := strings.Index(block, "do high")
	mi := strings.Index(block, "do mid")
	lo := strings.Index(block, "do low")
	if hi >= mi || mi >= lo {
		t.Errorf("not confidence-sorted: hi=%d mi=%d lo=%d\n%s", hi, mi, lo, block)
	}
}

func TestBuild_DropsBelowFloor(t *testing.T) {
	project := []*homunculus.Instinct{
		mkI("keep", 0.70, "keep me"),
		mkI("drop", 0.30, "drop me"),
	}
	block, ids := Build(project, nil, Options{})
	if len(ids) != 1 {
		t.Errorf("included = %d, want 1 (low-conf dropped)", len(ids))
	}
	if strings.Contains(block, "drop me") {
		t.Errorf("below-floor instinct was injected:\n%s", block)
	}
}

func TestBuild_ProjectBeatsGlobalAtEqualConfidence(t *testing.T) {
	project := []*homunculus.Instinct{mkI("proj", 0.80, "project rule")}
	global := []*homunculus.Instinct{mkI("glob", 0.80, "global rule")}
	block, _ := Build(project, global, Options{})
	if strings.Index(block, "project rule") > strings.Index(block, "global rule") {
		t.Errorf("project should rank before global at equal confidence:\n%s", block)
	}
}

func TestBuild_ProjectShadowsGlobalOnIDCollision(t *testing.T) {
	// Same ID in both corpora (the promotion case): the project version
	// is injected once, the global twin is suppressed — no duplicate
	// line, even though the global copy has higher confidence.
	project := []*homunculus.Instinct{mkI("shared", 0.70, "project version")}
	global := []*homunculus.Instinct{
		mkI("shared", 0.95, "global version"),
		mkI("global-only", 0.80, "global only rule"),
	}
	block, ids := Build(project, global, Options{})
	if len(ids) != 2 {
		t.Fatalf("included = %d, want 2 (shared deduped, global-only kept)", len(ids))
	}
	if strings.Contains(block, "global version") {
		t.Errorf("global twin of a project instinct must be suppressed:\n%s", block)
	}
	if !strings.Contains(block, "project version") {
		t.Errorf("project version of the shared instinct must be injected:\n%s", block)
	}
	if !strings.Contains(block, "global only rule") {
		t.Errorf("a global-only instinct must still be injected:\n%s", block)
	}
}

func TestBuild_BelowFloorProjectDoesNotShadowGlobal(t *testing.T) {
	// A project instinct that has independently decayed below the floor
	// (e.g. via session evaluation correcting it after promotion) must
	// NOT suppress its global twin: session evaluation only ever adjusts
	// the project-scope copy, so the global copy can still be valid,
	// cross-project-validated knowledge. The floor drops the weak project
	// copy from the pool, but the healthy global copy still injects.
	project := []*homunculus.Instinct{mkI("shared", 0.30, "weak project version")}
	global := []*homunculus.Instinct{mkI("shared", 0.95, "strong global version")}
	block, ids := Build(project, global, Options{})
	if len(ids) != 1 {
		t.Fatalf("included = %d, want 1 (global twin still injected)", len(ids))
	}
	if !strings.Contains(block, "strong global version") {
		t.Errorf("a below-floor project instinct must not shadow its still-valid global twin:\n%s", block)
	}
	if strings.Contains(block, "weak project version") {
		t.Errorf("the below-floor project instinct itself must not be injected:\n%s", block)
	}
}

func TestBuild_ByteCap(t *testing.T) {
	// 100 instincts, tiny cap → only a few fit.
	project := make([]*homunculus.Instinct, 100)
	for i := range project {
		project[i] = mkI("instinct-"+string(rune('a'+i%26)), 0.80, strings.Repeat("x", 50))
	}
	block, ids := Build(project, nil, Options{MaxBytes: 300, MaxInstincts: 100})
	if len(block) > 350 { // header + a few lines, well under the 100-instinct full render
		t.Errorf("block exceeded byte budget: %d bytes", len(block))
	}
	if len(ids) == 0 || len(ids) == 100 {
		t.Errorf("byte cap did not bound the count: n=%d", len(ids))
	}
}

func TestBuild_EmptyWhenNothingClearsFloor(t *testing.T) {
	project := []*homunculus.Instinct{mkI("x", 0.30, "y")}
	block, ids := Build(project, nil, Options{})
	if len(ids) != 0 || block != "" {
		t.Errorf("expected empty block, got n=%d block=%q", len(ids), block)
	}
}

func TestRenderInstinctLine(t *testing.T) {
	in := mkI("io-data-layer", 0.85, "Wrap I/O in the data layer.")
	line := renderInstinctLine(in)
	if !strings.Contains(line, "[85%]") {
		t.Errorf("missing confidence: %q", line)
	}
	if !strings.Contains(line, "Wrap I/O in the data layer.") {
		t.Errorf("missing action: %q", line)
	}
	if strings.Count(line, "\n") != 1 {
		t.Errorf("line should end with exactly one newline: %q", line)
	}
}
