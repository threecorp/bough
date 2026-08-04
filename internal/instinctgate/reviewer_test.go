package instinctgate

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
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
// simulating a model. The votes for one candidate are taken CONCURRENTLY,
// so the cursor is mutex-guarded; which goroutine draws which answer is
// then unspecified, and every assertion here is on the tally rather than
// on a per-vote position.
type scriptedCalls struct {
	mu sync.Mutex
	n  int
}

func (s *scriptedCalls) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func scriptedReviewer(answers ...func() ([]byte, error)) (ReviewFunc, *scriptedCalls) {
	calls := &scriptedCalls{}
	fn := func(_ context.Context, _, _ string) ([]byte, error) {
		calls.mu.Lock()
		i := calls.n
		calls.n++
		calls.mu.Unlock()
		if i >= len(answers) {
			i = len(answers) - 1
		}
		return answers[i]()
	}
	return fn, calls
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
	if got := calls.count(); got != 3 {
		t.Errorf("calls = %d, want 3 independent votes", got)
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

// TestProviderEnvelopeIsNotAPass is the regression net for a real defect
// found only by running the binary end-to-end: the CLI wrapper returned
// the provider's ENVELOPE ({"type":"result", …}) instead of the
// unwrapped verdict. JSON decoding ignores unknown fields, so it parsed
// cleanly as violation=false — the judge reported a full review while
// having judged nothing, and every prose-shaped violation sailed through.
//
// A document with no `violation` key is no answer, so it must fail open
// LOUDLY rather than read as a pass.
func TestProviderEnvelopeIsNotAPass(t *testing.T) {
	// The provider's envelope: well-formed JSON, carries the real verdict
	// only INSIDE .result, and has no `violation` key of its own.
	envelope := func() ([]byte, error) {
		return []byte("{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false," +
			"\"result\":\"```json\\n{\\\"violation\\\":true}\\n```\"}"), nil
	}
	fn, _ := scriptedReviewer(envelope, envelope, envelope)
	got := NewReviewer(fn).Review(context.Background(), proseCandidate())
	if !got.Failed {
		t.Errorf("an envelope must not count as a verdict: %+v", got)
	}
	if got.Err == nil {
		t.Error("the unusable verdict must be reported")
	}
}

// TestExplicitFalseIsStillAVerdict pins the other side of the pointer:
// an explicit violation:false is a real answer and must be counted,
// otherwise every clean instinct would fail open instead of clearing.
func TestExplicitFalseIsStillAVerdict(t *testing.T) {
	fn, _ := scriptedReviewer(ok(false, ""), ok(false, ""), ok(false, ""))
	got := NewReviewer(fn).Review(context.Background(), proseCandidate())
	if got.Failed {
		t.Errorf("explicit false is a verdict, not a failure: %+v", got)
	}
	if got.Violation {
		t.Errorf("three clean votes must clear: %+v", got)
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
	got := NewReviewer(fn).ReviewBatch(context.Background(), []Candidate{
		cand("clean", "when tests are flaky", "re-run with a fixed seed"),
		proseCandidate(),
	})
	if len(got.Held) != 0 {
		t.Errorf("held = %+v, want none", got.Held)
	}
	if got.Reviewed != 1 || got.Failed != 1 {
		t.Errorf("reviewed=%d failed=%d, want 1/1", got.Reviewed, got.Failed)
	}
	if got.FirstErr == nil {
		t.Error("the first failure must be surfaced")
	}
	// The unreviewed candidate is NAMED, not just counted — a caller that
	// only gets a number cannot act on it.
	if len(got.Unreviewed) != 1 || got.Unreviewed[0] != "tidy-stale-branch" {
		t.Errorf("Unreviewed = %v, want the prose candidate's id", got.Unreviewed)
	}
	if got.Cancelled {
		t.Error("a provider failure is not a cancellation")
	}
}

// TestBudgetExhaustionIsReportedNotHidden pins the arithmetic behind the
// cap-vs-outage distinction. With 3 votes and a 10-call budget the batch
// degrades at candidate 4: it wins one slot, and one usable vote is below
// the 2-vote agreement threshold, so it fails open — as does everything
// after it. The caller must be able to see WHICH candidates went unjudged
// and get the underlying error, or "unreviewed=N" is indistinguishable
// from the model being down.
func TestBudgetExhaustionIsReportedNotHidden(t *testing.T) {
	var errBudget = errors.New("self-DoS limit exceeded")
	budget := 10
	var mu sync.Mutex
	fn := func(_ context.Context, _, _ string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if budget <= 0 {
			return nil, errBudget
		}
		budget--
		return verdictJSON(false, ""), nil
	}
	cands := make([]Candidate, 0, 6)
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5", "c6"} {
		cands = append(cands, cand(id, "when something happens", "do the ordinary thing"))
	}
	got := NewReviewer(fn).ReviewBatch(context.Background(), cands)

	if got.Reviewed != 3 {
		t.Errorf("Reviewed = %d, want 3 — a 10-call budget covers exactly 3 candidates at 3 votes each", got.Reviewed)
	}
	if got.Failed != 3 {
		t.Errorf("Failed = %d, want 3 (candidates 4-6)", got.Failed)
	}
	// Named, not just counted: a caller handed a bare number cannot report
	// which instincts entered the corpus unjudged.
	want := []string{"c4", "c5", "c6"}
	if len(got.Unreviewed) != len(want) {
		t.Fatalf("Unreviewed = %v, want %v", got.Unreviewed, want)
	}
	for i := range want {
		if got.Unreviewed[i] != want[i] {
			t.Errorf("Unreviewed = %v, want %v", got.Unreviewed, want)
			break
		}
	}
	// The underlying error must survive so the CLI can classify it as a
	// budget cap rather than an outage.
	if !errors.Is(got.FirstErr, errBudget) {
		t.Errorf("FirstErr = %v, want it to wrap the budget error", got.FirstErr)
	}
	if got.Cancelled {
		t.Error("budget exhaustion is not a cancellation")
	}
}

// TestCancellationIsNotFailOpen is the invariant that keeps an interrupt
// from silently promoting the rest of a batch. Fail-open exists so a model
// outage cannot stop the corpus growing; an operator pressing Ctrl-C has
// not asked for every remaining candidate to be accepted unjudged.
func TestCancellationIsNotFailOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fn, calls := scriptedReviewer(ok(false, ""))
	got := NewReviewer(fn).ReviewBatch(ctx, []Candidate{
		cand("a", "when tests are flaky", "re-run with a fixed seed"),
		cand("b", "when the build is slow", "profile the slowest package"),
		proseCandidate(),
	})
	if !got.Cancelled {
		t.Fatalf("a cancelled context must be reported as cancelled: %+v", got)
	}
	if got.Reviewed != 0 || got.Failed != 3 {
		t.Errorf("reviewed=%d failed=%d, want 0/3 — nothing was judged", got.Reviewed, got.Failed)
	}
	if len(got.Unreviewed) != 3 {
		t.Errorf("every candidate must be named unreviewed, got %v", got.Unreviewed)
	}
	// And it must not have spent a single provider call doing it.
	if n := calls.count(); n != 0 {
		t.Errorf("calls = %d, want 0 — a dead context must not burn budget", n)
	}
}

// citedVerdictJSON builds one vote's raw response for the grounding tests.
func citedVerdictJSON(violation bool, rule, category, quote string) []byte {
	b, _ := json.Marshal(map[string]any{
		"violation": violation, "rule": rule, "category": category, "quote": quote,
	})
	return b
}

// TestHallucinatedCitationReleasesTheHold: consensus defeats random
// variance, not a hallucination the model commits to across votes. A
// cited category that is not on the list the judge was given must
// RELEASE the note — held on a rule nobody configured is held on
// nothing — and the release must be counted, because a grounding check
// that never fires reads exactly like an inert one.
func TestHallucinatedCitationReleasesTheHold(t *testing.T) {
	rv := NewReviewer(func(_ context.Context, _, _ string) ([]byte, error) {
		return citedVerdictJSON(true, "made-up-rule", "a category nobody configured", ""), nil
	})
	rv.Votes, rv.Agree = 1, 1
	rv.Categories = []string{"deleting a branch, tag, or remote ref"}

	br := rv.ReviewBatch(context.Background(), []Candidate{{ID: "n1", Trigger: "t", Action: "a"}})
	if len(br.Held) != 0 {
		t.Fatalf("a hallucinated citation must not hold, got %v", br.Held)
	}
	if br.RuleUngrounded != 1 {
		t.Errorf("the release must be counted, got rule_ungrounded=%d", br.RuleUngrounded)
	}
	if br.Reviewed != 1 {
		t.Errorf("a released note was still reviewed, got reviewed=%d", br.Reviewed)
	}
}

// TestVerbatimCitationHolds: the happy path — a category copied from
// the list grounds, and the hold stands. Whitespace and case wobble
// must not read as hallucination.
func TestVerbatimCitationHolds(t *testing.T) {
	rv := NewReviewer(func(_ context.Context, _, _ string) ([]byte, error) {
		return citedVerdictJSON(true, "no-branch-delete", "Deleting a branch,  tag, or remote ref", "delete the branch"), nil
	})
	rv.Votes, rv.Agree = 1, 1
	rv.Categories = []string{"deleting a branch, tag, or remote ref"}

	br := rv.ReviewBatch(context.Background(), []Candidate{{ID: "n1", Trigger: "when done", Action: "delete the branch afterwards"}})
	if len(br.Held) != 1 {
		t.Fatalf("a grounded citation must hold, got %v (ungrounded=%d)", br.Held, br.RuleUngrounded)
	}
	if br.QuoteUnverified != 0 {
		t.Errorf("the quote IS in the action; quote_unverified=%d", br.QuoteUnverified)
	}
}

// TestUnlocatableQuoteFlagsButDoesNotRelease: the consensus judged the
// whole surface, so a quote that cannot be found flags the hold for a
// closer look rather than undoing the judgement.
func TestUnlocatableQuoteFlagsButDoesNotRelease(t *testing.T) {
	rv := NewReviewer(func(_ context.Context, _, _ string) ([]byte, error) {
		return citedVerdictJSON(true, "no-branch-delete", "deleting a branch, tag, or remote ref", "words that appear nowhere"), nil
	})
	rv.Votes, rv.Agree = 1, 1
	rv.Categories = []string{"deleting a branch, tag, or remote ref"}

	br := rv.ReviewBatch(context.Background(), []Candidate{{ID: "n1", Trigger: "when done", Action: "delete the branch afterwards"}})
	if len(br.Held) != 1 {
		t.Fatalf("an unlocatable quote must not release the hold, got %v", br.Held)
	}
	if br.QuoteUnverified != 1 {
		t.Errorf("the unlocatable quote must be flagged, got %d", br.QuoteUnverified)
	}
}

// TestEmptyCitationIsNotAHallucination: omitting the citation is "no
// answer", not a fabricated one — the hold stands. Releasing on an
// omitted field would let a lazy model nullify every hold.
func TestEmptyCitationIsNotAHallucination(t *testing.T) {
	rv := NewReviewer(func(_ context.Context, _, _ string) ([]byte, error) {
		return citedVerdictJSON(true, "no-branch-delete", "", ""), nil
	})
	rv.Votes, rv.Agree = 1, 1
	rv.Categories = []string{"deleting a branch, tag, or remote ref"}

	br := rv.ReviewBatch(context.Background(), []Candidate{{ID: "n1", Trigger: "t", Action: "a"}})
	if len(br.Held) != 1 || br.RuleUngrounded != 0 {
		t.Errorf("an omitted citation keeps the hold, got held=%v ungrounded=%d", br.Held, br.RuleUngrounded)
	}
}

// TestOneSloppyVoteDoesNotReleaseWhenAnotherGrounds: the citation is
// per-vote. One vote paraphrasing the category must not release a note
// that another agreeing vote cited verbatim — measured on the real
// model, that released a genuine violation once in 13 candidates.
func TestOneSloppyVoteDoesNotReleaseWhenAnotherGrounds(t *testing.T) {
	answers := [][]byte{
		citedVerdictJSON(true, "r", "a paraphrase nobody configured", ""),
		citedVerdictJSON(true, "r", "deleting a branch, tag, or remote ref", ""),
		citedVerdictJSON(true, "r", "deleting a branch, tag, or remote ref", ""),
	}
	var mu sync.Mutex
	i := 0
	rv := NewReviewer(func(_ context.Context, _, _ string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		a := answers[i%len(answers)]
		i++
		return a, nil
	})
	rv.Categories = []string{"deleting a branch, tag, or remote ref"}

	br := rv.ReviewBatch(context.Background(), []Candidate{{ID: "n1", Trigger: "t", Action: "a"}})
	if len(br.Held) != 1 || br.RuleUngrounded != 0 {
		t.Errorf("a grounded agreeing vote must carry the hold, got held=%v ungrounded=%d", br.Held, br.RuleUngrounded)
	}
}
