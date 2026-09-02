package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/instinctgate"
	"github.com/ikeikeikeike/bough/internal/provider/claudecli"
)

// judgeFlagging returns a judge that calls a violation exactly when the
// action contains marker, plus a counter of how many candidates it was
// shown. The count is the load-bearing part: exemption is proved by the
// judge never SEEING a candidate, which a verdict-only assertion cannot
// distinguish from the judge seeing it and clearing it.
func judgeFlagging(marker string) (judgeFactory, func() int) {
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
		// 1-of-1 keeps these tests about WHICH candidates were judged
		// rather than about how consensus tallied the votes.
		r.Votes, r.Agree = 1, 1
		r.Categories = instinctgate.DefaultForbiddenActions
		return r, nil, nil
	}
	return factory, func() int { return seen }
}

// The allowlist is the operator's manual override for a note the patterns
// cannot help but match — typically one whose whole point IS the
// prohibition, so it quotes the command it forbids. The deterministic
// screen honours it by clearing the note, but Cleared is exactly the
// batch handed to the judge: without an exemption there too, the model
// re-judges the note and can quarantine it under a `judge:` rule the
// allowlist has no way to release. An escape hatch one layer honours and
// the next ignores is not an escape hatch.
func TestScreenAndPromote_AllowListExemptsFromTheJudgeToo(t *testing.T) {
	layout := homunculus.FromRoot(t.TempDir())
	ident := homunculus.ProjectIdentity{ID: "proj1", Name: "demo"}
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	staged := stagedFromResult(t, layout, ident, now)

	gate := instinctgate.New(instinctgate.Config{
		Enabled:  true,
		AllowIDs: []string{"merge-when-green"},
	})
	// The judge would hold the exempt note if it ever reached it.
	judge, judged := judgeFlagging("gh pr merge")

	out := screenAndPromote(context.Background(), layout, ident.ID, staged, gate, judge, now)

	if out.Quarantined != 0 {
		t.Errorf("quarantined %d, want 0 — the exempt note was judged anyway", out.Quarantined)
	}
	if out.Emitted != 2 {
		t.Errorf("emitted %d, want both notes promoted", out.Emitted)
	}
	// The non-exempt note IS still judged: a judge that saw nothing would
	// pass the assertions above for the wrong reason.
	if got := judged(); got != 1 {
		t.Errorf("judge saw %d candidates, want exactly 1 (the non-exempt note)", got)
	}
	if out.ReviewCandidates != 1 {
		t.Errorf("ReviewCandidates = %d, want 1 — exempt notes must not count as reviewed",
			out.ReviewCandidates)
	}
	got, _ := homunculus.ScanInstincts(layout.InstinctsDir(ident.ID))
	if len(got) != 2 {
		t.Errorf("personal corpus has %d instincts, want 2", len(got))
	}
}

// The other half: with nothing exempted the judge really does hold, so
// the test above cannot be passing merely because the judge is inert.
// Here the judge flags the note the PATTERNS clear, which also pins the
// division of labour — the deterministic layer holds the command-shaped
// one, the judge holds the other.
func TestScreenAndPromote_JudgeHoldsWhatThePatternsClear(t *testing.T) {
	layout := homunculus.FromRoot(t.TempDir())
	ident := homunculus.ProjectIdentity{ID: "proj1", Name: "demo"}
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	staged := stagedFromResult(t, layout, ident, now)

	gate := instinctgate.New(instinctgate.Config{Enabled: true})
	judge, judged := judgeFlagging("Read the surrounding implementation")

	out := screenAndPromote(context.Background(), layout, ident.ID, staged, gate, judge, now)

	if got := judged(); got != 1 {
		t.Fatalf("judge saw %d candidates, want 1 (the one the patterns cleared)", got)
	}
	if out.Quarantined != 2 {
		t.Errorf("quarantined %d, want 2 (one by pattern, one by judge)", out.Quarantined)
	}
	if out.Emitted != 0 {
		t.Errorf("emitted %d, want 0", out.Emitted)
	}
}
