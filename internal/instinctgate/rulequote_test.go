package instinctgate

import (
	"context"
	"encoding/json"
	"testing"
)

// verdictWithRuleQuote is a violating verdict that cites a category AND
// a forbidding sentence — the shape the v3 prompt asks for.
func verdictWithRuleQuote(category, ruleQuote string) func() ([]byte, error) {
	return func() ([]byte, error) {
		v := true
		b, _ := json.Marshal(reviewVerdict{
			Violation: &v,
			Rule:      "never-discard-wip",
			Reason:    "instructs destroying uncommitted work",
			Category:  category,
			Quote:     "clear out whatever is lying around",
			RuleQuote: ruleQuote,
		})
		return b, nil
	}
}

func ruleGovernance(t *testing.T) *Governance {
	t.Helper()
	dir := t.TempDir()
	writeGovDoc(t, dir, "RULES.md",
		"# Project rules\n\n"+
			"Uncommitted work is never discarded without an explicit instruction.\n"+
			"Every change must be reviewed by a second engineer before merge.\n")
	return LoadGovernance([]string{dir})
}

func writeGovDoc(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := writeFile(dir, name, body); err != nil {
		t.Fatal(err)
	}
}

// A hold whose cited rule really appears in the governance documents
// stands. This is the half consensus CAN carry.
func TestRuleQuoteFoundInGovernanceHolds(t *testing.T) {
	cat := DefaultForbiddenActions[1] // the discard-WIP family
	fn, _ := scriptedReviewer(verdictWithRuleQuote(cat,
		"Uncommitted work is never discarded without an explicit instruction."))
	r := NewReviewer(fn)
	r.Votes, r.Agree = 1, 1
	r.Categories = DefaultForbiddenActions
	r.Governance = ruleGovernance(t)

	got := r.Review(context.Background(), proseCandidate())
	if !got.Violation {
		t.Errorf("a hold citing a real governance sentence must stand: %+v", got)
	}
	if got.RuleUngrounded {
		t.Errorf("a real citation must not be reported ungrounded: %+v", got)
	}
}

// The failure this exists for: consensus cannot catch a hallucination,
// because the same model on the same fixed text invents the same rule
// every time. The governance corpus is fixed, so the citation is
// checkable without another model call — and an invented sentence
// RELEASES the hold rather than quarantining on a rule nobody wrote.
func TestInventedRuleQuoteReleasesTheHold(t *testing.T) {
	cat := DefaultForbiddenActions[1]
	fn, _ := scriptedReviewer(verdictWithRuleQuote(cat,
		"Contributors must obtain written approval from the platform team before editing any file."))
	r := NewReviewer(fn)
	r.Votes, r.Agree = 1, 1
	r.Categories = DefaultForbiddenActions
	r.Governance = ruleGovernance(t)

	got := r.Review(context.Background(), proseCandidate())
	if got.Violation {
		t.Errorf("a hold resting on an invented rule must be released: %+v", got)
	}
	if !got.RuleUngrounded {
		t.Errorf("the release must be reported as ungrounded, not as a clean pass: %+v", got)
	}
}

// An empty rule_quote is not a fabrication — the prompt tells the judge
// to leave it empty rather than paraphrase. This check can only ever
// RELEASE a hold, so silence must not release one. Governance.Grounded
// carries it: a citation shorter than groundingRunLength is grounded,
// and nothing is shorter than nothing.
func TestEmptyRuleQuoteDoesNotReleaseTheHold(t *testing.T) {
	cat := DefaultForbiddenActions[1]
	fn, _ := scriptedReviewer(verdictWithRuleQuote(cat, ""))
	r := NewReviewer(fn)
	r.Votes, r.Agree = 1, 1
	r.Categories = DefaultForbiddenActions
	r.Governance = ruleGovernance(t)

	got := r.Review(context.Background(), proseCandidate())
	if !got.Violation {
		t.Errorf("an empty rule_quote must leave the hold standing: %+v", got)
	}
}

// With no corpus loaded the check is off: a project with no rule
// documents has nothing to ground against, and grounding every citation
// as unfounded would disable the judge entirely.
func TestNoGovernanceLeavesTheCheckOff(t *testing.T) {
	cat := DefaultForbiddenActions[1]
	fn, _ := scriptedReviewer(verdictWithRuleQuote(cat, "A sentence from nowhere at all."))
	r := NewReviewer(fn)
	r.Votes, r.Agree = 1, 1
	r.Categories = DefaultForbiddenActions
	// r.Governance deliberately nil.

	got := r.Review(context.Background(), proseCandidate())
	if !got.Violation {
		t.Errorf("with no corpus the hold must stand: %+v", got)
	}
}
