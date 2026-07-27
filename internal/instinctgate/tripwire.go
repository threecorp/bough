// Package instinctgate is the generation gate: the checks a freshly
// minted instinct must clear before it is allowed to become injectable.
//
// A learning system whose writer is an LLM and whose checker is a fixed
// string list has a direction problem — the writer has more vocabulary
// than any pattern list, so paraphrase escapes a substring check forever.
// This package is the deterministic half of the answer: a small, honest
// set of patterns over the command-shaped forbidden actions (never merge
// unasked, never discard working state, never rewrite commit identity,
// never force-push, never push straight to a protected branch). It is the
// backstop a later LLM judge fails open onto, NOT a claim of completeness:
// measured elsewhere, a pattern layer of this shape scored 11/11 on
// command-shaped violations and 0/8 on prose-shaped intent. Ship it for the
// command forms it does catch; the prose forms are the judge's job.
//
// The matcher lives here once and is used from every site that needs it
// (the mint write path, the promote path, a verify command) so the three
// cannot drift apart.
package instinctgate

import "regexp"

// A Tripwire is one named forbidden-command pattern. The name is what a
// quarantine REPORT cites, so it must read as the rule, not the regex.
type Tripwire struct {
	Rule string
	Re   *regexp.Regexp
}

// DefaultTripwires returns the built-in command-shaped forbidden-action
// patterns. They are universal git/VCS-safety rules, not project-specific,
// so they ship as bough defaults.
//
// Every pattern is argument-order tolerant: it anchors on the verb, not on
// a fixed flag order, so `git merge --no-ff` and `git --no-ff merge` both
// hit. The verb is matched as a standalone word — `merge ` or end-of-line,
// never `merge-base`/`mergetool` — because RE2 has no lookahead, so the
// boundary is expressed as "followed by whitespace or end" rather than a
// negative class.
func DefaultTripwires() []Tripwire {
	// verb builds "<tool> … <verb><boundary>" — anchored on tool and verb,
	// tolerant of any flags between or after them. The trailing boundary is
	// "a non-word, non-hyphen char, or end of line" so the verb is matched
	// standalone: it fires on `merge`, `merge ` and `merge` inside backticks,
	// but not on a longer word like `merge-base` or `mergetool` (RE2 has no
	// lookahead, so the boundary consumes one char rather than asserting).
	verb := func(rule, tool, v string) Tripwire {
		return Tripwire{Rule: rule, Re: regexp.MustCompile(`(?i)\b` + tool + `\b[^\n]*\b` + v + `(?:[^-\w]|$)`)}
	}
	return []Tripwire{
		// Merge a PR / branch without being asked to.
		verb("never-merge-unasked", "gh", "pr\\s+merge"),
		verb("never-merge-unasked", "git", "merge"),
		// Discard uncommitted / in-progress work.
		{Rule: "never-discard-wip", Re: regexp.MustCompile(`(?i)\bgit\b[^\n]*\breset\b[^\n]*--hard\b`)},
		{Rule: "never-discard-wip", Re: regexp.MustCompile(`(?i)\bgit\b[^\n]*\bcheckout\b[^\n]*\bHEAD\b[^\n]*--`)},
		{Rule: "never-discard-wip", Re: regexp.MustCompile(`(?i)\bgit\s+stash\s+(?:drop|clear)\b`)},
		{Rule: "never-discard-wip", Re: regexp.MustCompile(`(?i)\bgit\s+clean\b[^\n]*-[a-z]*f`)},
		// Forced branch delete, SHORT form. The flag letter is matched
		// case-SENSITIVELY (the `(?-i:…)` group re-enables case) because
		// `git branch -d` is the safe delete-if-merged form: folding case
		// here would quarantine a routine-cleanup instinct under a rule
		// about discarding work.
		//
		// The dash must OPEN a token — whitespace or a quote/backtick — so
		// a branch NAME carrying `-D`
		// (`git branch -d F-Deploy`) is not read as the flag. The letter
		// may sit anywhere inside a combined cluster (`-D`, `-Dq`, `-qD`)
		// because git's option parser accepts them in any order; anchoring
		// it at the END of the cluster silently missed `-Dq`.
		{Rule: "never-discard-wip", Re: regexp.MustCompile("(?i)\\bgit\\s+branch\\b[^\\n]*[\\s\"'`]-(?-i:[a-zA-Z]*D[a-zA-Z]*)\\b")},
		// Forced branch delete, LONG form. `--delete --force` is
		// definitionally `-D`, and the previous case-insensitive pattern
		// caught it only by accident (the `d` of `--delete`). Both flag
		// orders are spelled out because RE2 has no lookahead to express
		// "contains both" more compactly.
		{Rule: "never-discard-wip", Re: regexp.MustCompile(`(?i)\bgit\s+branch\b(?:[^\n]*--delete\b[^\n]*--force\b|[^\n]*--force\b[^\n]*--delete\b)`)},
		// Rewrite commit identity / history the operator owns.
		{Rule: "never-override-author", Re: regexp.MustCompile(`(?i)\bgit\b[^\n]*(?:--author=|-c\s+user\.(?:email|name))`)},
		// Force-push (over a shared/protected ref).
		{Rule: "never-force-push", Re: regexp.MustCompile(`(?i)\bgit\s+push\b[^\n]*(?:--force\b|--force-with-lease\b|-[a-zA-Z]*f)`)},
		// Delete a remote branch.
		{Rule: "never-delete-remote-branch", Re: regexp.MustCompile(`(?i)\bgit\s+push\b[^\n]*--delete\b`)},
	}
}
