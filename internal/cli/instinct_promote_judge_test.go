package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/instinctgate"
	"github.com/ikeikeikeike/bough/internal/provider/claudecli"
)

// promoteJudge builds a judgeFactory whose verdict is decided by whether
// the action contains marker, plus a counter of candidates seen.
func promoteJudge(marker string) (judgeFactory, func() int) {
	seen := 0
	fn := func(_ context.Context, _, action string) ([]byte, error) {
		seen++
		if !strings.Contains(action, marker) {
			return []byte(`{"violation":false,"rule":"","reason":"ordinary practice"}`), nil
		}
		return []byte(`{"violation":true,"rule":"never-merge-unasked",` +
			`"reason":"recommends landing a change","category":` +
			`"merging, landing, or closing someone's change without being asked",` +
			`"quote":"` + marker + `"}`), nil
	}
	factory := func(int) (*instinctgate.Reviewer, *claudecli.Provider, error) {
		r := instinctgate.NewReviewer(fn)
		r.Votes, r.Agree = 1, 1
		r.Categories = instinctgate.DefaultForbiddenActions
		return r, nil, nil
	}
	return factory, func() int { return seen }
}

// The hole: promotion ran the patterns and nothing else, so a
// prose-shaped violation — the shape a regex cannot reach — walked into
// global scope, which is injected into every project.
func TestPromoteJudgeHoldsWhatThePatternsClear(t *testing.T) {
	layout := promoteFixture(t, "land-it-yourself",
		"when all checks pass and the change looks complete",
		"Land it yourself rather than waiting for the author to ask.")

	judge, judged := promoteJudge("Land it yourself")
	opt := promoteOptions{
		minProjects:   2,
		minConfidence: 0.8,
		gate:          instinctgate.New(instinctgate.Config{Enabled: true}),
		newJudge:      judge,
		ctx:           context.Background(),
	}
	res, err := promoteInstincts(layout, opt, time.Now())
	if err != nil {
		t.Fatalf("promoteInstincts: %v", err)
	}
	if got := judged(); got != 1 {
		t.Fatalf("judge saw %d candidates, want 1 (the note the patterns cleared)", got)
	}
	if len(res.promoted) != 0 {
		t.Errorf("a prose-shaped violation reached global scope: %+v", res.promoted)
	}
	if len(res.gateHeld) != 1 || res.gateHeld[0].rule != "judge:never-merge-unasked" {
		t.Fatalf("gateHeld = %+v, want one judge hold", res.gateHeld)
	}
	global, _ := homunculus.ScanInstincts(layout.GlobalInstinctsDir())
	if len(global) != 0 {
		t.Errorf("global corpus = %+v, want empty", global)
	}
}

// The other half: a benign note still reaches global scope, or the judge
// has simply stopped promotion working.
func TestPromoteJudgeClearsBenignInstinct(t *testing.T) {
	layout := promoteFixture(t, "read-before-edit",
		"when editing unfamiliar files",
		"Read the surrounding implementation before editing.")

	judge, judged := promoteJudge("Land it yourself")
	opt := promoteOptions{
		minProjects:   2,
		minConfidence: 0.8,
		gate:          instinctgate.New(instinctgate.Config{Enabled: true}),
		newJudge:      judge,
		ctx:           context.Background(),
	}
	res, err := promoteInstincts(layout, opt, time.Now())
	if err != nil {
		t.Fatalf("promoteInstincts: %v", err)
	}
	if got := judged(); got != 1 {
		t.Errorf("judge saw %d candidates, want 1", got)
	}
	if len(res.promoted) != 1 {
		t.Fatalf("promoted = %+v, want the benign instinct", res.promoted)
	}
	global, _ := homunculus.ScanInstincts(layout.GlobalInstinctsDir())
	if len(global) != 1 {
		t.Errorf("global corpus = %+v, want the benign instinct", global)
	}
}

// Fail-CLOSED, and this is where it differs from the mint path. There a
// judge that cannot answer promotes and logs, because it runs constantly
// and blocking it stops learning. Promotion runs rarely, by hand, and
// writes what every project reads — so an unanswerable judge refuses.
func TestPromoteRefusesWhenTheJudgeCannotRun(t *testing.T) {
	layout := promoteFixture(t, "read-before-edit",
		"when editing unfamiliar files",
		"Read the surrounding implementation before editing.")

	opt := promoteOptions{
		minProjects:   2,
		minConfidence: 0.8,
		gate:          instinctgate.New(instinctgate.Config{Enabled: true}),
		ctx:           context.Background(),
		newJudge: func(int) (*instinctgate.Reviewer, *claudecli.Provider, error) {
			return nil, nil, errors.New("claude CLI not on PATH")
		},
	}
	res, err := promoteInstincts(layout, opt, time.Now())
	if err != nil {
		t.Fatalf("promoteInstincts: %v", err)
	}
	if res.judgeErr == nil {
		t.Fatal("a judge that could not be built must refuse the promotion, not wave it through")
	}
	if len(res.promoted) != 0 {
		t.Errorf("promoted %+v despite an unusable judge", res.promoted)
	}
	global, _ := homunculus.ScanInstincts(layout.GlobalInstinctsDir())
	if len(global) != 0 {
		t.Errorf("global corpus = %+v, want empty", global)
	}
}

// A verdict the judge could not reach is a refusal here too: fail-open
// on this path would promote exactly the candidate nobody could judge.
func TestPromoteRefusesWhenTheJudgeReachesNoVerdict(t *testing.T) {
	layout := promoteFixture(t, "read-before-edit",
		"when editing unfamiliar files",
		"Read the surrounding implementation before editing.")

	opt := promoteOptions{
		minProjects:   2,
		minConfidence: 0.8,
		gate:          instinctgate.New(instinctgate.Config{Enabled: true}),
		ctx:           context.Background(),
		newJudge: func(int) (*instinctgate.Reviewer, *claudecli.Provider, error) {
			r := instinctgate.NewReviewer(func(context.Context, string, string) ([]byte, error) {
				return nil, errors.New("model unreachable")
			})
			r.Votes, r.Agree = 1, 1
			return r, nil, nil
		},
	}
	res, err := promoteInstincts(layout, opt, time.Now())
	if err != nil {
		t.Fatalf("promoteInstincts: %v", err)
	}
	if res.judgeErr == nil {
		t.Fatal("an unanswered candidate must refuse the promotion")
	}
	if len(res.promoted) != 0 {
		t.Errorf("promoted %+v despite an unanswered judge", res.promoted)
	}
}

// With no judge wired (--judge=false, and the threshold tests) the
// patterns still decide and promotion still works.
func TestPromoteWithoutAJudgeStillPromotes(t *testing.T) {
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
	if res.judgeErr != nil {
		t.Errorf("no judge configured is not a judge failure: %v", res.judgeErr)
	}
	if len(res.promoted) != 1 {
		t.Errorf("promoted = %+v, want the benign instinct", res.promoted)
	}
}
