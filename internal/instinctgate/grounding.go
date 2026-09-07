package instinctgate

import (
	"os"
	"path/filepath"
	"strings"
)

// Rule grounding is the JUDGE's check, not the deterministic gate's. It
// answers one question: does a cited rule actually exist in the
// governance text? The reference design applies it to the judge's own
// citation — a rule the judge cannot ground is a hallucination, and a
// hold resting on an invented rule is DROPPED, because quarantining on
// one erodes trust in every real hold and consensus cannot catch it (the
// same model hallucinates the same rule every time).
//
// This package once ran the check inside the deterministic gate too,
// with the OPPOSITE polarity: a candidate whose own text sounded like
// governance ("must never …") but shared no run with the corpus was
// HELD. The reference has no such layer, and in one live corpus it
// quarantined five notes about mutation testing — each saying a gate
// "must never silently pass" about itself, which no governance document
// happens to phrase. Sounding like a rule is not a violation; the gate
// holds only on tripwires and the denylist now.
//
// A contiguous run, not a bag of words: paraphrase is exactly what
// hallucination looks like, and any overlap measure that tolerates
// reordering would accept "two approvals are required before merging"
// against a document that says "approvals" and "merge" in unrelated
// sentences.

// groundingRunLength is the number of consecutive words that must
// appear verbatim in the governance text. Five is long enough that
// matching by coincidence is unlikely across a few thousand words, and
// short enough that a genuine citation survives light rewording at its
// edges.
const groundingRunLength = 5

// groundingMinShortQuote is the character floor for a quote too short to
// carry a full run. Below it a "citation" is a fragment that would match
// almost any prose, so it counts as no citation at all.
const groundingMinShortQuote = 12

// Governance is the project's actual rule text, loaded once and reused
// across a batch. Sources records where it came from so a report can
// say what an instinct was grounded against — "we found no rule" is
// only actionable if the operator knows which documents were read.
type Governance struct {
	// norm is the corpus folded once for matching. Grounded compares
	// against this string rather than a word index because the reference
	// implementation matches by substring on the folded text, and the
	// two disagree on tokens the fold keeps (`--force` stays one token
	// there, becomes `force` under a strip-everything tokenizer).
	norm    string
	Sources []string
	// raw is the corpus verbatim, for the prompt. The judge is asked to
	// quote the forbidding sentence FROM these documents, so it has to be
	// shown them: a citation cannot be verified against a text the judge
	// never saw, it can only be invented.
	raw string
}

// Text returns the governance corpus verbatim, or "" when none loaded.
func (g *Governance) Text() string {
	if g == nil {
		return ""
	}
	return g.raw
}

// LoadGovernance reads the governance documents at the given paths.
// Directories are walked one level for .md files. Missing paths are
// skipped silently: a project with no CLAUDE.md has no governance to
// contradict, and the layer simply stays inert (Active reports false).
func LoadGovernance(paths []string) *Governance {
	g := &Governance{}
	var b strings.Builder
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if info.IsDir() {
			entries, derr := os.ReadDir(p)
			if derr != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
					continue
				}
				full := filepath.Join(p, e.Name())
				if raw, rerr := os.ReadFile(full); rerr == nil {
					b.Write(raw)
					b.WriteByte('\n')
					g.Sources = append(g.Sources, full)
				}
			}
			continue
		}
		if raw, rerr := os.ReadFile(p); rerr == nil {
			b.Write(raw)
			b.WriteByte('\n')
			g.Sources = append(g.Sources, p)
		}
	}
	g.raw = b.String()
	g.norm = normRule(g.raw)
	return g
}

// Active reports whether any governance text was loaded. With none, the
// layer must not hold anything: every citation would look unfounded,
// and a guard that rejects everything is indistinguishable from a
// broken one.
func (g *Governance) Active() bool { return g != nil && g.norm != "" }

// Grounded reports whether the cited rule really appears in the
// governance corpus, matched by a contiguous run of groundingRunLength
// words. A run, not a bag of words: paraphrase is what hallucination
// looks like, and any measure tolerating reordering would accept "two
// approvals are required before merging" against a document that says
// "approvals" and "merge" in unrelated sentences.
//
// A quote too short to carry a run must instead appear whole AND be at
// least groundingMinShortQuote characters. A handful of words is not
// evidence that a rule exists, so brevity does not earn a pass — an
// EMPTY quote fails here and releases the hold, which is the contract:
// the judge is told not to report a violation it cannot cite.
func (g *Governance) Grounded(text string) bool {
	if !g.Active() {
		return true // nothing to ground against ⇒ nothing to contradict
	}
	q := normRule(text)
	words := strings.Fields(q)
	if len(words) < groundingRunLength {
		return len(q) >= groundingMinShortQuote && strings.Contains(g.norm, q)
	}
	for i := 0; i+groundingRunLength <= len(words); i++ {
		if strings.Contains(g.norm, strings.Join(words[i:i+groundingRunLength], " ")) {
			return true
		}
	}
	return false
}

// normRule folds the differences a model introduces when copying a
// sentence out of markdown — back-ticks, quotes, emphasis, rewrapping —
// and nothing else. Punctuation and hyphens are KEPT: stripping them
// would let a cited `git push --force` match prose that merely says
// "force", which is the looseness the run-length rule exists to avoid.
func normRule(s string) string {
	folded := strings.Map(func(r rune) rune {
		switch r {
		case '`', '"', '\'', '*', '_':
			return ' '
		}
		return r
	}, strings.ToLower(s))
	return strings.Join(strings.Fields(folded), " ")
}
