// Package retrieve ranks instincts against the current prompt.
//
// It exists because ranking by a model-assigned confidence score fails
// in a way that looks fine from the inside. Measured on this project's
// live corpus: 117 instincts, every one at confidence 0.85 — zero bits
// of entropy, a single distinct value. The "ranking" is a total tie, so
// the real order is whatever breaks the tie (here: id, ascending), and
// the top-N cut then keeps the same alphabetical prefix on every prompt.
// Everything after it is unreachable at any relevance, forever.
//
// The replacement fuses three channels by rank:
//
//	exact    identifiers and paths the prompt names verbatim — the
//	         strongest signal available and purely lexical
//	lexical  BM25 over the whole instinct text — catches the rest
//	         without embeddings
//	recency  modification time — a weak prior only
//
// Fused with Reciprocal Rank Fusion (Cormack et al. 2009): the channels
// have incomparable scales and RRF needs only ranks, so nothing has to
// be calibrated against anything else.
//
// The rule that makes zero results possible: a candidate present in
// neither the exact nor the lexical channel is dropped even if recency
// ranks it. Without that, an off-topic prompt fills the budget with
// whatever was written most recently, which is how a retrieval system
// convinces its operator it is working when it is not.
//
// Confidence is still stored and still shown — it is audit data. It is
// simply not a ranking key.
package retrieve

import (
	"regexp"
	"strings"
)

// identifierRe matches candidate identifier shapes: dotted/qualified
// names (pkg.Func, a.b.c), paths, snake and kebab names, and plain
// words. Whether a candidate actually COUNTS as an identifier is decided
// by hasIdentifierSignal below — the regex is deliberately permissive so
// the judgement lives in one readable place.
var identifierRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:[./-][A-Za-z0-9_]+)+|[A-Za-z_][A-Za-z0-9_]*`)

// hasIdentifierSignal decides whether a token is code-shaped rather than
// an ordinary English word. Without this the "exact" channel degenerates
// into a second bag-of-words: every prose word ("schema", "change")
// would score as an exact hit, the two channels would agree on almost
// everything, and fusing them would add nothing over BM25 alone.
//
// A token counts when it carries a structural marker a person would not
// type in prose: a separator (dot, slash, dash, underscore), an internal
// capital (CamelCase), or a digit.
func hasIdentifierSignal(raw string) bool {
	hasUpperInside := false
	for i, r := range raw {
		switch {
		case r == '.' || r == '/' || r == '-' || r == '_':
			return true
		case r >= '0' && r <= '9':
			return true
		case i > 0 && r >= 'A' && r <= 'Z':
			hasUpperInside = true
		}
	}
	return hasUpperInside
}

// stopwords are function words that carry no retrieval signal. They are
// removed from the LEXICAL channel so an off-topic prompt cannot qualify
// a document by sharing "in" or "the" with it — BM25 gives such a match
// a tiny but non-zero score, and non-zero is what the drop rule keys on.
//
// This is emphatically NOT a length rule. Dropping ≤2-char tokens is one
// of the bugs being fixed here: "PR" and "CI" are content words, and a
// query whose only content words are short must stay answerable. The
// list below is curated by function, not by size.
var stopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {},
	"but": {}, "by": {}, "can": {}, "do": {}, "for": {}, "from": {},
	"how": {}, "i": {}, "if": {}, "in": {}, "is": {}, "it": {}, "of": {},
	"on": {}, "or": {}, "should": {}, "so": {}, "that": {}, "the": {},
	"then": {}, "this": {}, "to": {}, "use": {}, "was": {}, "what": {},
	"when": {}, "where": {}, "which": {}, "with": {}, "you": {},
}

// ContentTokens is Tokens minus the stopwords: the form the lexical
// channel scores against.
func ContentTokens(text string) map[string]struct{} {
	all := Tokens(text)
	for w := range stopwords {
		delete(all, w)
	}
	return all
}

// splitRe breaks free text into words. Unlike the clustering tokenizer
// this one keeps SHORT tokens: a prompt whose only content words are
// two-character terms ("PR", "CI") must remain answerable. Dropping
// ≤2-char tokens on both sides makes such a query unsatisfiable by
// construction — it can never match anything, and the failure looks like
// "no relevant instincts" rather than like a bug.
var splitRe = regexp.MustCompile(`[^A-Za-z0-9_./-]+`)

// Tokens returns the searchable tokens of a text: every word, plus, for
// each identifier-shaped token, its normalised head form and its parts.
//
// The head form matters because identifiers are written with their call
// arguments or receivers in prose — `Assert(cap, id)` or `layout.Dir()`
// — while a person types the bare name. Indexing the decorated form
// only means the bare query never matches: unsatisfiable by
// construction, again silently.
//
// The parts matter for the same reason in reverse: a query naming
// `homunculus.Layout` must reach a note that says only `Layout`, so the
// dotted token contributes both its whole and its segments. A relevance
// floor that compared dotted query tokens against a tokenizer which
// stripped dots could never be satisfied.
func Tokens(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, w := range splitRe.Split(text, -1) {
		w = strings.Trim(w, "./-_")
		if w == "" {
			continue
		}
		lower := strings.ToLower(w)
		out[lower] = struct{}{}
		for _, part := range strings.FieldsFunc(lower, func(r rune) bool {
			return r == '.' || r == '/' || r == '-'
		}) {
			if part != "" {
				out[part] = struct{}{}
			}
		}
	}
	return out
}

// Identifiers extracts the identifier-shaped tokens of a text in
// normalised head form: lower-cased, stripped of call syntax and
// trailing punctuation. These feed the exact-match channel, where a hit
// is a much stronger signal than a bag-of-words overlap.
func Identifiers(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, m := range identifierRe.FindAllString(text, -1) {
		if !hasIdentifierSignal(m) {
			continue // an ordinary word is not an exact-match signal
		}
		head := normalizeIdentifier(m)
		if head == "" {
			continue
		}
		out[head] = struct{}{}
		// A qualified name also registers its last segment, so a note
		// about `homunculus.Layout` is reachable by someone typing
		// `Layout` — the way the symbol is usually spoken.
		if i := strings.LastIndexAny(head, "./"); i >= 0 && i+1 < len(head) {
			out[head[i+1:]] = struct{}{}
		}
	}
	return out
}

// Jaccard returns |a ∩ b| / |a ∪ b| over two token sets. Two empty sets
// return 0: contentless items are not "similar", they are both noise.
//
// It lives here, in the leaf package that owns tokenization, because
// both consumers need the same answer over the same kind of set — the
// clustering pipeline measures cohesion with it, and the injector uses
// it to drop a selected line that merely restates a higher-ranked one.
// Two copies would be two chances for "similar" to mean two things.
func Jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	// Iterate the smaller set for the intersection scan.
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	inter := 0
	for k := range small {
		if _, ok := large[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// normalizeIdentifier reduces one raw match to its head form: lower
// case, no call parentheses, no trailing separator. `Assert(cap,` and
// `assert` must resolve to the same token or neither side can ever match
// the other.
func normalizeIdentifier(s string) string {
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, "./-_,;:")
	return strings.ToLower(s)
}
