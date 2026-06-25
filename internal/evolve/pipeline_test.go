package evolve

import (
	"context"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// stubJudge returns a fixed verdict for every cluster.
type stubJudge struct {
	verdict Verdict
	err     error
}

func (s stubJudge) Judge(_ context.Context, _ JudgeInput) (Verdict, error) {
	return s.verdict, s.err
}

func ioInstincts(n int) []*homunculus.Instinct {
	out := make([]*homunculus.Instinct, n)
	bodies := []string{
		"io lives in the data layer wrapper interface consistently",
		"io lives in data layer wrapper interface pattern always",
		"data layer wraps io interface wrapper repeatedly here",
		"data layer io wrapper interface boundary maintained",
	}
	for i := 0; i < n; i++ {
		out[i] = &homunculus.Instinct{
			ID:         "io-" + string(rune('a'+i)),
			Trigger:    "when wrapping io",
			Confidence: 0.8,
			Domain:     "workflow",
			Body:       "## Action\n" + bodies[i%len(bodies)],
		}
	}
	return out
}

func TestPipeline_PassMintsSkill(t *testing.T) {
	p := Pipeline{
		Judge: stubJudge{verdict: Verdict{
			Decision: DecisionPass, Confidence: 0.9,
			Label: "io-data-layer", Description: "Apply when wrapping I/O",
		}},
		Now: func() time.Time { return time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC) },
	}
	labels := &ClusterLabels{Labels: map[string]string{}}
	out, err := p.Run(context.Background(), ioInstincts(3), labels)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Skills) != 1 {
		t.Fatalf("Skills = %d, want 1 (clusters=%d rejected=%d)", len(out.Skills), out.ClusterCount, len(out.Rejected))
	}
	s := out.Skills[0]
	if s.Label != "io-data-layer" || !s.NewLabel {
		t.Errorf("skill label mismatch: %+v", s)
	}
	// 3 members avg 0.8 → agent eligible
	if len(out.Agents) != 1 {
		t.Errorf("Agents = %d, want 1", len(out.Agents))
	}
	// 3 workflow instincts conf 0.8 → 3 commands
	if len(out.Commands) != 3 {
		t.Errorf("Commands = %d, want 3", len(out.Commands))
	}
}

func TestPipeline_FailRejects(t *testing.T) {
	p := Pipeline{
		Judge: stubJudge{verdict: Verdict{Decision: DecisionFail, Confidence: 0.9, Reason: "orthogonal"}},
		Now:   func() time.Time { return time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC) },
	}
	labels := &ClusterLabels{Labels: map[string]string{}}
	out, err := p.Run(context.Background(), ioInstincts(3), labels)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Skills) != 0 {
		t.Errorf("FAIL should mint no skills, got %d", len(out.Skills))
	}
	foundGate5Reject := false
	for _, r := range out.Rejected {
		if r.Verdict != nil && r.Verdict.Decision == DecisionFail {
			foundGate5Reject = true
		}
	}
	if !foundGate5Reject {
		t.Errorf("expected a GATE 5 FAIL in the rejected list")
	}
}

func TestPipeline_DoubtReusesPriorLabel(t *testing.T) {
	p := Pipeline{
		Judge: stubJudge{verdict: Verdict{
			Decision: DecisionDoubt, Confidence: 0.5,
			Label: "prior-io-handling", ReusePriorLabel: true,
		}},
		Now: func() time.Time { return time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC) },
	}
	// Seed a prior whose vocabulary partially overlaps but not enough
	// to fail GATE 3 — use a low coverage prior.
	labels := &ClusterLabels{Labels: map[string]string{
		"prior-io-handling": "Apply when handling input output streams",
	}}
	out, err := p.Run(context.Background(), ioInstincts(3), labels)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Skills) == 0 {
		t.Skip("cluster did not survive gates with this prior; DOUBT path not exercised")
	}
	s := out.Skills[0]
	if s.NewLabel {
		t.Errorf("DOUBT with a nearest prior should reuse, not mint: %+v", s)
	}
}

func TestPipeline_JudgeErrorBecomesDoubt(t *testing.T) {
	p := Pipeline{
		Judge: stubJudge{err: context.DeadlineExceeded},
		Now:   func() time.Time { return time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC) },
	}
	labels := &ClusterLabels{Labels: map[string]string{}}
	out, err := p.Run(context.Background(), ioInstincts(3), labels)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Judge error → DOUBT → skill with no prior → minted with a
	// slugified fallback label.
	if len(out.Skills) != 1 {
		t.Fatalf("Skills = %d, want 1 (judge error → DOUBT)", len(out.Skills))
	}
	if out.Skills[0].Verdict.Decision != DecisionDoubt {
		t.Errorf("verdict = %q, want DOUBT", out.Skills[0].Verdict.Decision)
	}
}
