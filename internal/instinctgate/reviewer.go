package instinctgate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// The deterministic layers catch command SHAPES. They cannot catch
// intent expressed as prose — "when the branch looks stale, tidy it up
// before continuing" recommends discarding work without naming a single
// command, and no pattern list will ever cover the paraphrases. That is
// what this layer is for, and it is the reason the pattern layer is
// documented as a backstop rather than as coverage.
//
// Three properties make it safe to put a model in a guard:
//
//   - It runs LAST, on what the deterministic layers already cleared, so
//     a model outage cannot un-catch a command-shaped violation.
//   - It FAILS OPEN, loudly. A dead guard that silently held everything
//     would stop the corpus growing and look like "the LLM found
//     nothing"; a learner that cannot learn is worse than one with a
//     weaker check. Errors clear the candidate and are reported.
//   - It votes. A single sample is a coin flip on borderline text, so
//     the verdict is a majority of independent votes; consensus reduces
//     VARIANCE, which is the only thing it can fix. It does not make the
//     model more correct, so it is not a substitute for grounding.

// ReviewFunc asks the model to judge one instinct's propagating surface.
// The implementation renders a prompt, spawns the provider, and returns
// the raw structured-output bytes. Declaring the function type here (and
// wiring claudecli in the CLI layer) keeps this package free of any
// provider import and unit-testable with a plain closure — the same
// inversion the evolve judge uses.
//
// It MUST be safe to call concurrently: Review takes its votes in
// parallel, so an implementation that closes over mutable per-call state
// (a counter, a cache, an appended audit slice) will corrupt it. This is
// not theoretical — the package's own test double had to grow a mutex
// when the votes stopped being serial. It must also honour ctx: the
// provider acquires a rate-limit slot before it consults ctx, so a call
// that ignores cancellation still spends budget.
type ReviewFunc func(ctx context.Context, trigger, action string) (raw []byte, err error)

// reviewVerdict is the model's structured answer. Violation names the
// forbidden action the instinct recommends; Reason is shown to the
// operator in the quarantine report.
//
// Violation is a POINTER so an absent key is distinguishable from an
// explicit false. JSON decoding ignores unknown fields, so any
// well-formed JSON that simply is not a verdict — a provider envelope, an
// error document, a truncated response — would otherwise decode cleanly
// as "no violation" and the judge would report a full review while
// having judged nothing. A missing key is no answer, not a pass.
type reviewVerdict struct {
	Violation *bool  `json:"violation"`
	Rule      string `json:"rule"`
	Reason    string `json:"reason"`
	// Category is the forbidden action the judge matched, copied
	// verbatim from the list it was given. It is the judge's CITATION,
	// and it is verified: a category that is not on the list is a
	// hallucinated citation, and a note must not be held on one.
	Category string `json:"category"`
	// Quote is the offending span, copied verbatim from the instinct.
	// Verified against the text; a quote that cannot be located does not
	// release the hold — the consensus still stands — but it is flagged,
	// because a hold whose evidence cannot be found needs a closer look.
	Quote string `json:"quote"`
}

// Reviewer runs the consensus vote. Votes and Agree are struct fields
// rather than package constants so a test can run a deterministic 1-of-1
// vote and an operator can trade cost against variance.
type Reviewer struct {
	review ReviewFunc
	// Votes is how many independent samples are taken per candidate.
	Votes int
	// Agree is how many of them must call it a violation before the
	// instinct is held. Requiring a majority rather than any single vote
	// keeps one unlucky sample from quarantining a good instinct.
	Agree int
	// Categories is the forbidden-action list the judge was prompted
	// with, used to GROUND its citation. Consensus defeats random
	// variance; it cannot defeat a hallucination the model commits to,
	// which is what grounding is for — the two are not substitutes.
	// Empty disables the check (nothing to ground against).
	Categories []string
}

// DefaultVotes is how many independent samples NewReviewer takes per
// candidate. Exported because a caller sizing an LLM budget has to
// multiply by it, and hard-coding 3 at the call site is how the two drift
// apart — which is exactly the arithmetic that silently truncated a
// review batch.
const DefaultVotes = 3

// NewReviewer wires a 3-vote / 2-agree reviewer.
func NewReviewer(review ReviewFunc) *Reviewer {
	return &Reviewer{review: review, Votes: DefaultVotes, Agree: 2}
}

// ReviewResult is one candidate's outcome. Failed records that the layer
// could not reach a verdict — the candidate is cleared (fail-open), and
// the caller MUST surface the count so an unreviewed pass is never
// mistaken for a clean one.
type ReviewResult struct {
	ID        string
	Violation bool
	Rule      string
	Reason    string
	Failed    bool
	// RuleUngrounded marks a consensus violation that was RELEASED
	// because the winning vote's citation was not on the category list —
	// a hallucinated rule must not hold a note. Violation is false when
	// this is set; the release is the outcome, this field is the audit.
	RuleUngrounded bool
	// QuoteUnverified marks a hold whose quoted evidence could not be
	// located in the instinct. The hold STANDS — the consensus is about
	// the text, not the quote — but the operator reviewing it should
	// know the cited words were not found.
	QuoteUnverified bool
	// Errs holds EVERY vote's error, not just the first. Which vote index
	// happened to fail first says nothing about which failure matters: an
	// unrelated transient error on vote 0 would otherwise hide a rate-limit
	// trip on vote 1, and the caller — the only layer that knows the
	// provider's sentinel errors — could not classify what went wrong.
	Errs []error
	// Cancelled marks a Failed result caused by ctx cancellation rather
	// than by the judge being unable to answer. The two must not be
	// conflated: fail-open is for a model outage, and silently promoting
	// the rest of a batch because the operator pressed Ctrl-C is not a
	// judgement anyone made.
	Cancelled bool
	Err       error
}

// Review votes on one candidate. Any vote that errors or fails to parse
// is discarded rather than counted as either verdict; if too few usable
// votes remain to reach Agree, the result is Failed (fail-open).
func (r *Reviewer) Review(ctx context.Context, c Candidate) ReviewResult {
	votes := r.Votes
	if votes < 1 {
		votes = 1
	}
	agree := r.Agree
	if agree < 1 || agree > votes {
		agree = (votes / 2) + 1
	}

	// An already-cancelled context must not spend a single call. Checked
	// before the fan-out because Generate acquires a limiter slot before
	// it looks at ctx, so votes launched into a dead context still burn
	// the self-DoS budget and record failures.
	if err := ctx.Err(); err != nil {
		return ReviewResult{ID: c.ID, Failed: true, Cancelled: true, Err: err}
	}

	// The votes are independent by construction, so they are taken
	// CONCURRENTLY: run serially, a 3-vote review costs three round trips
	// per candidate and the gate's latency is what an operator feels on
	// every mint. Results are collected into a fixed-index slice and
	// tallied in vote order afterwards, so the verdict — including which
	// rule and reason are reported — does not depend on completion order.
	type voteResult struct {
		raw []byte
		err error
	}
	results := make([]voteResult, votes)
	var wg sync.WaitGroup
	for i := range votes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := r.review(ctx, c.Trigger, c.Action)
			results[i] = voteResult{raw: raw, err: err}
		}()
	}
	wg.Wait()

	// Cancellation during the fan-out is NOT the same as a judge that
	// could not answer. Fail-open exists so a model outage cannot stop the
	// corpus growing; an interrupted operator has not asked for every
	// remaining candidate to be promoted unjudged.
	if err := ctx.Err(); err != nil {
		return ReviewResult{ID: c.ID, Failed: true, Cancelled: true, Err: err}
	}

	usable := 0
	var errs []error
	var violating []reviewVerdict
	for _, res := range results {
		if res.err != nil {
			errs = append(errs, res.err)
			continue
		}
		var v reviewVerdict
		if err := json.Unmarshal(res.raw, &v); err != nil {
			errs = append(errs, fmt.Errorf("parse verdict: %w (raw=%q)", err, res.raw))
			continue
		}
		if v.Violation == nil {
			// Parsed, but not a verdict — no `violation` key. Counting this
			// as a pass is how a broken judge reports a clean corpus.
			errs = append(errs, fmt.Errorf("verdict has no %q field (raw=%q)", "violation", res.raw))
			continue
		}
		usable++
		if *v.Violation {
			violating = append(violating, v)
		}
	}

	// Not enough usable votes to reach the agreement threshold either
	// way ⇒ no verdict. Clear it and say so.
	if usable < agree {
		return ReviewResult{ID: c.ID, Failed: true, Err: firstOf(errs), Errs: errs}
	}
	if len(violating) >= agree {
		// Ground the citations before acting on the verdict. Consensus
		// defeats random variance; it cannot defeat a hallucination the
		// model commits to, which is what grounding is for. The citation
		// is PER-VOTE, so the verdict carries the first vote whose
		// citation grounds (an empty citation grounds trivially — nothing
		// was cited): one vote paraphrasing the category must not release
		// a note that another agreeing vote cited verbatim. Measured on
		// the real model: the first violating vote paraphrased once in 13
		// candidates, and grounding only that vote released a genuine
		// violation. Only when EVERY agreeing vote cites something not on
		// the list is the note released — held on a rule nobody configured
		// is held on nothing.
		chosen := -1
		for i, v := range violating {
			if v.Category == "" || r.groundedCategory(v.Category) {
				chosen = i
				break
			}
		}
		if chosen == -1 {
			return ReviewResult{ID: c.ID, RuleUngrounded: true, Rule: violating[0].Rule, Reason: violating[0].Reason}
		}
		v := violating[chosen]
		rule := v.Rule
		if rule == "" {
			rule = "judge-flagged"
		}
		// The quote is verified but never decisive: the consensus judged
		// the whole surface, so an unlocatable quote flags the hold for a
		// closer look rather than undoing the judgement.
		unverified := v.Quote != "" && !containsNormalized(c.Trigger+"\n"+c.Action, v.Quote)
		return ReviewResult{ID: c.ID, Violation: true, Rule: rule, Reason: v.Reason, QuoteUnverified: unverified}
	}
	return ReviewResult{ID: c.ID}
}

// groundedCategory reports whether the judge's citation is one of the
// categories it was prompted with. Whitespace-normalized, case-folded:
// the prompt says "copied verbatim", and the tolerance covers line
// wrapping, not paraphrase — a paraphrased citation is treated as not
// on the list, which errs toward releasing, the fail-open direction.
func (r *Reviewer) groundedCategory(category string) bool {
	if len(r.Categories) == 0 {
		return true // nothing to ground against — the check is off
	}
	want := normalizeSpace(category)
	for _, c := range r.Categories {
		if strings.EqualFold(normalizeSpace(c), want) {
			return true
		}
	}
	return false
}

// containsNormalized reports whether needle occurs in haystack after
// whitespace normalization on both sides, so a quote that wrapped
// differently in the model's output still verifies.
func containsNormalized(haystack, needle string) bool {
	return strings.Contains(
		strings.ToLower(normalizeSpace(haystack)),
		strings.ToLower(normalizeSpace(needle)),
	)
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// BatchResult is one batch's outcome. Reviewed counts the candidates that
// actually got a verdict and Failed counts those that did not — reporting
// both is the contract that keeps the layer honest, because "0 violations"
// means nothing without "out of how many reviewed".
//
// Unreviewed lists the ids that reached no verdict, so the caller can act
// on them rather than only counting them. Cancelled says the batch was
// interrupted, which is a different fact from "the judge could not answer"
// and needs a different response.
type BatchResult struct {
	Held       []Decision
	Reviewed   int
	Failed     int
	Unreviewed []string
	Cancelled  bool
	// RuleUngrounded counts consensus violations RELEASED because the
	// judge's citation was not on the category list. A permanently-zero
	// count is itself suspect — it is how an inert check reads — so it
	// travels to telemetry rather than living only in stdout.
	RuleUngrounded int
	// QuoteUnverified counts holds whose quoted evidence could not be
	// located. The holds stand; the count tells the reviewer where to
	// look harder.
	QuoteUnverified int
	FirstErr        error
	// Errs is every vote error across the batch. The caller classifies
	// them — it is the layer that knows the provider's sentinels — and a
	// single FirstErr cannot carry that: whichever vote index failed first
	// would decide, hiding a budget trip behind an unrelated hiccup.
	Errs []error
}

// firstOf returns the first error or nil, so a single-error field can
// still be offered alongside the full list.
func firstOf(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return errs[0]
}

// ReviewBatch votes on every candidate and partitions them. Cleared
// includes the fail-open ones by construction.
//
// It STOPS at the first cancellation instead of running the remaining
// candidates into a dead context: every one of those would fail open, and
// a caller that promotes on fail-open would land the whole tail of the
// batch unjudged because someone pressed Ctrl-C.
func (r *Reviewer) ReviewBatch(ctx context.Context, cands []Candidate) BatchResult {
	var out BatchResult
	for i, c := range cands {
		res := r.Review(ctx, c)
		out.Errs = append(out.Errs, res.Errs...)
		if res.Cancelled {
			out.Cancelled = true
			if out.FirstErr == nil {
				out.FirstErr = res.Err
			}
			// This candidate and every one after it reached no verdict.
			for _, rest := range cands[i:] {
				out.Failed++
				out.Unreviewed = append(out.Unreviewed, rest.ID)
			}
			return out
		}
		switch {
		case res.Failed:
			out.Failed++
			out.Unreviewed = append(out.Unreviewed, res.ID)
			if out.FirstErr == nil {
				out.FirstErr = res.Err
			}
		case res.RuleUngrounded:
			// Released, and counted: a hallucinated citation must neither
			// hold the note nor vanish from the record.
			out.Reviewed++
			out.RuleUngrounded++
		case res.Violation:
			out.Reviewed++
			if res.QuoteUnverified {
				out.QuoteUnverified++
			}
			out.Held = append(out.Held, Decision{ID: res.ID, Rule: "judge:" + res.Rule})
		default:
			out.Reviewed++
		}
	}
	return out
}
