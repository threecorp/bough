package instinctgate

import (
	"os"
	"path/filepath"
	"strings"
)

// Rule grounding catches the failure mode where a minted instinct
// SOUNDS like governance but is not: "the team requires two approvals
// before merge" when no such rule exists anywhere. An LLM writing from
// session traces will confidently produce these, and once one is in the
// corpus it is injected as if it were policy — the learner teaches its
// own hallucination back to itself.
//
// The check is deliberately narrow. Only instincts that CLAIM to be
// citing a rule are grounded, because a note recording an ordinary
// practice ("re-run flaky tests with a fixed seed") is not asserting
// governance and has nothing to be grounded against. For those that do
// claim it, the assertion must share a contiguous run of words with the
// project's actual governance text.
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

// ruleClaimMarkers are the phrasings a minted instinct uses when it is
// asserting governance rather than recording a practice. Matching one
// is what puts an instinct in scope for grounding at all.
var ruleClaimMarkers = []string{
	"the rule", "per the", "policy", "governance", "mandated", "mandatory",
	"is required by", "as required", "the convention is", "must always",
	"must never", "is forbidden", "prohibited", "is not allowed",
}

// Governance is the project's actual rule text, loaded once and reused
// across a batch. Sources records where it came from so a report can
// say what an instinct was grounded against — "we found no rule" is
// only actionable if the operator knows which documents were read.
type Governance struct {
	words []string
	// index maps each governance word to every position it occupies, so a
	// candidate run can jump straight to its possible starts. It is built
	// ONCE with the corpus rather than per Grounded call: the corpus is a
	// few thousand words and a batch screens many candidates, so rebuilding
	// it per call re-tokenized the whole corpus once per instinct.
	index   map[string][]int
	Sources []string
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
	g.words = normalizeWords(b.String())
	g.index = make(map[string][]int, len(g.words))
	for i, w := range g.words {
		g.index[w] = append(g.index[w], i)
	}
	return g
}

// Active reports whether any governance text was loaded. With none, the
// layer must not hold anything: every citation would look unfounded,
// and a guard that rejects everything is indistinguishable from a
// broken one.
func (g *Governance) Active() bool { return g != nil && len(g.words) > 0 }

// ClaimsRule reports whether the text is asserting governance (and so
// is in scope for grounding) rather than recording a practice.
func ClaimsRule(text string) bool {
	lower := strings.ToLower(text)
	for _, m := range ruleClaimMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// Grounded reports whether text shares a contiguous run of
// groundingRunLength words with the governance corpus. Text shorter
// than the run length is treated as grounded: it is too short to be
// judged, and holding it would punish brevity rather than invention.
func (g *Governance) Grounded(text string) bool {
	if !g.Active() {
		return true // nothing to ground against ⇒ nothing to contradict
	}
	claim := normalizeWords(text)
	if len(claim) < groundingRunLength {
		return true
	}
	for i := 0; i+groundingRunLength <= len(claim); i++ {
		for _, start := range g.index[claim[i]] {
			if runMatches(g.words, claim[i:i+groundingRunLength], start) {
				return true
			}
		}
	}
	return false
}

// runMatches reports whether run appears in words starting at start.
func runMatches(words, run []string, start int) bool {
	if start+len(run) > len(words) {
		return false
	}
	for k, w := range run {
		if words[start+k] != w {
			return false
		}
	}
	return true
}

// normalizeWords lowercases and strips punctuation so a citation
// matches its source across quoting and formatting differences —
// markdown emphasis, back-ticks, and trailing commas must not decide
// whether a rule is considered grounded.
func normalizeWords(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		isLetter := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		return !isLetter && !isDigit
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}
