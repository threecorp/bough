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
	block, n := Build(project, nil, Options{})
	if n != 3 {
		t.Fatalf("included = %d, want 3", n)
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
	block, n := Build(project, nil, Options{})
	if n != 1 {
		t.Errorf("included = %d, want 1 (low-conf dropped)", n)
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

func TestBuild_ByteCap(t *testing.T) {
	// 100 instincts, tiny cap → only a few fit.
	project := make([]*homunculus.Instinct, 100)
	for i := range project {
		project[i] = mkI("instinct-"+string(rune('a'+i%26)), 0.80, strings.Repeat("x", 50))
	}
	block, n := Build(project, nil, Options{MaxBytes: 300, MaxInstincts: 100})
	if len(block) > 350 { // header + a few lines, well under the 100-instinct full render
		t.Errorf("block exceeded byte budget: %d bytes", len(block))
	}
	if n == 0 || n == 100 {
		t.Errorf("byte cap did not bound the count: n=%d", n)
	}
}

func TestBuild_EmptyWhenNothingClearsFloor(t *testing.T) {
	project := []*homunculus.Instinct{mkI("x", 0.30, "y")}
	block, n := Build(project, nil, Options{})
	if n != 0 || block != "" {
		t.Errorf("expected empty block, got n=%d block=%q", n, block)
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
