package instinctgate

import (
	"context"
	"errors"
	"testing"
)

// verdictJSON builds a model answer.
func verdictJSON(violation bool, rule string) []byte {
	if violation {
		return []byte(`{"violation":true,"rule":"` + rule + `","reason":"recommends discarding uncommitted work"}`)
	}
	return []byte(`{"violation":false}`)
}

// scriptedReviewer returns a ReviewFunc that replays the given answers
// in order, so a test can state the vote sequence directly instead of
// simulating a model.
func scriptedReviewer(answers ...func() ([]byte, error)) (ReviewFunc, *int) {
	calls := 0
	fn := func(_ context.Context, _, _ string) ([]byte, error) {
		i := calls
		calls++
		if i >= len(answers) {
			i = len(answers) - 1
		}
		return answers[i]()
	}
	return fn, &calls
}

func ok(v bool, rule string) func() ([]byte, error) {
	return func() ([]byte, error) { return verdictJSON(v, rule), nil }
}

func boom() func() ([]byte, error) {
	return func() ([]byte, error) { return nil, errors.New("provider unavailable") }
}

func garbage() func() ([]byte, error) {
	return func() ([]byte, error) { return []byte("not json at all"), nil }
}

// proseCandidate is the case the deterministic layers provably cannot
// catch: it recommends discarding uncommitted work without naming a
// single command, so no pattern list matches it.
func proseCandidate() Candidate {
	return cand("tidy-stale-branch",
		"when the branch looks stale before continuing",
		"clear out whatever is lying around in the working tree so you start from a clean slate")
}

// TestProseViolationIsInvisibleToPatterns pins WHY this layer exists: it
// proves the deterministic gate clears the prose-shaped violation, so
// the judge is covering a real gap rather than duplicating the tripwires.
func TestProseViolationIsInvisibleToPatterns(t *testing.T) {
	res := New(Config{Enabled: true}).Screen([]Candidate{proseCandidate()})
	if len(res.Held) != 0 {
		t.Fatalf("precondition failed: the pattern layer caught the prose case (%+v) — pick a probe it genuinely misses", res.Held)
	}
}

// TestConsensusMajorityHolds pins the vote: two of three saying
// violation is enough to hold.
func TestConsensusMajorityHolds(t *testing.T) {
	fn, calls := scriptedReviewer(ok(true, "never-discard-wip"), ok(false, ""), ok(true, "never-discard-wip"))
	got := NewReviewer(fn).Review(context.Background(), proseCandidate())
	if !got.Violation || got.Rule != "never-discard-wip" {
		t.Errorf("2-of-3 violation votes should hold: %+v", got)
	}
	if *calls != 3 {
		t.Errorf("calls = %d, want 3 independent votes", *calls)
	}
}

// TestConsensusMinorityClears pins the other side: one unlucky sample
// must not quarantine a good instinct — that is the variance consensus
// exists to absorb.
func TestConsensusMinorityClears(t *testing.T) {
	fn, _ := scriptedReviewer(ok(true, "never-discard-wip"), ok(false, ""), ok(false, ""))
	got := NewReviewer(fn).Review(context.Background(), proseCandidate())
	if got.Violation {
		t.Errorf("1-of-3 should clear, got %+v", got)
	}
	if got.Failed {
		t.Errorf("three usable votes is a verdict, not a failure: %+v", got)
	}
}

// TestJudgeFailsOpenLoudly is the invariant that keeps a dead guard from
// silently strangling the corpus: an unreachable provider clears the
// candidate AND reports the failure, so "nothing was held" can never be
// confused with "nothing was checked".
func TestJudgeFailsOpenLoudly(t *testing.T) {
	fn, _ := scriptedReviewer(boom(), boom(), boom())
	got := NewReviewer(fn).Review(context.Background(), proseCandidate())
	if got.Violation {
		t.Error("an unreachable judge must not hold the candidate")
	}
	if !got.Failed || got.Err == nil {
		t.Errorf("the failure must be reported, not swallowed: %+v", got)
	}
}

// TestUnparseableVotesDoNotCountAsClean pins that garbage is discarded
// rather than read as "no violation" — counting a malformed answer as a
// pass is how a broken judge reports a clean corpus.
func TestUnparseableVotesDoNotCountAsClean(t *testing.T) {
	fn, _ := scriptedReviewer(garbage(), garbage(), garbage())
	got := NewReviewer(fn).Review(context.Background(), proseCandidate())
	if !got.Failed {
		t.Errorf("all-garbage votes must be a failure, not a clean pass: %+v", got)
	}
}

// TestPartialFailureStillReachesVerdict pins that one flaky call does
// not discard the whole review when the surviving votes already settle
// it.
func TestPartialFailureStillReachesVerdict(t *testing.T) {
	fn, _ := scriptedReviewer(boom(), ok(true, "never-discard-wip"), ok(true, "never-discard-wip"))
	got := NewReviewer(fn).Review(context.Background(), proseCandidate())
	if !got.Violation || got.Failed {
		t.Errorf("2 usable violation votes should hold despite 1 error: %+v", got)
	}
}

// TestReviewBatchReportsReviewedAndFailed pins the honesty contract:
// the caller gets both the violations and how many candidates actually
// received a verdict, because "0 held" is meaningless without it.
func TestReviewBatchReportsReviewedAndFailed(t *testing.T) {
	// First candidate: three clean votes. Second: three errors.
	seq := []func() ([]byte, error){
		ok(false, ""), ok(false, ""), ok(false, ""),
		boom(), boom(), boom(),
	}
	fn, _ := scriptedReviewer(seq...)
	held, reviewed, failed, err := NewReviewer(fn).ReviewBatch(context.Background(), []Candidate{
		cand("clean", "when tests are flaky", "re-run with a fixed seed"),
		proseCandidate(),
	})
	if len(held) != 0 {
		t.Errorf("held = %+v, want none", held)
	}
	if reviewed != 1 || failed != 1 {
		t.Errorf("reviewed=%d failed=%d, want 1/1", reviewed, failed)
	}
	if err == nil {
		t.Error("the first failure must be surfaced")
	}
}
