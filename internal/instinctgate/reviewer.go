package instinctgate

import (
	"context"
	"encoding/json"
	"fmt"
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
type ReviewFunc func(ctx context.Context, trigger, action string) (raw []byte, err error)

// reviewVerdict is the model's structured answer. Violation names the
// forbidden action the instinct recommends; Reason is shown to the
// operator in the quarantine report.
type reviewVerdict struct {
	Violation bool   `json:"violation"`
	Rule      string `json:"rule"`
	Reason    string `json:"reason"`
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
}

// NewReviewer wires a ReviewFunc into a 3-vote / 2-agree reviewer.
func NewReviewer(review ReviewFunc) *Reviewer {
	return &Reviewer{review: review, Votes: 3, Agree: 2}
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

	violations := 0
	usable := 0
	var firstErr error
	var rule, reason string
	for i := 0; i < votes; i++ {
		raw, err := r.review(ctx, c.Trigger, c.Action)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var v reviewVerdict
		if err := json.Unmarshal(raw, &v); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("parse verdict: %w (raw=%q)", err, raw)
			}
			continue
		}
		usable++
		if v.Violation {
			violations++
			if rule == "" {
				rule, reason = v.Rule, v.Reason
			}
		}
	}

	// Not enough usable votes to reach the agreement threshold either
	// way ⇒ no verdict. Clear it and say so.
	if usable < agree {
		return ReviewResult{ID: c.ID, Failed: true, Err: firstErr}
	}
	if violations >= agree {
		if rule == "" {
			rule = "judge-flagged"
		}
		return ReviewResult{ID: c.ID, Violation: true, Rule: rule, Reason: reason}
	}
	return ReviewResult{ID: c.ID}
}

// ReviewBatch votes on every candidate and partitions them. Cleared
// includes the fail-open ones by construction; Reviewed counts the
// candidates that actually got a verdict, and Failed counts those that
// did not. Returning both is the contract that keeps the layer honest:
// "0 violations" means nothing without "out of how many reviewed".
func (r *Reviewer) ReviewBatch(ctx context.Context, cands []Candidate) (held []Decision, reviewed, failed int, firstErr error) {
	for _, c := range cands {
		res := r.Review(ctx, c)
		switch {
		case res.Failed:
			failed++
			if firstErr == nil {
				firstErr = res.Err
			}
		case res.Violation:
			reviewed++
			held = append(held, Decision{ID: res.ID, Rule: "judge:" + res.Rule})
		default:
			reviewed++
		}
	}
	return held, reviewed, failed, firstErr
}
