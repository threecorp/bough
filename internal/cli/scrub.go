package cli

import (
	"encoding/json"
	"regexp"
)

// maxObsFieldChars caps any single string field in a stored
// observation, matching ECC observe.sh's 5000-char per-field truncation
// (observe.sh:188,194). The live observation file the observer tails is
// already bounded as a whole by rotateIfLarge (10 MiB); this bounds the
// individual fields so one giant Write/Read tool payload cannot bloat a
// single record (and the prompt the observer later renders from it).
const maxObsFieldChars = 5000

// truncMarker is appended to a string that was truncated so a reader can
// tell the value is incomplete.
const truncMarker = "…[truncated]"

// secretPattern is a verbatim port of ECC continuous-learning-v2
// observe.sh's redaction regex (observe.sh:268-283). It matches a
// secret-like key name, the separators that follow (quote / colon /
// equals / whitespace), an optional scheme word (e.g. "Bearer "), then
// the secret token (8+ token chars), and redacts only the token —
// keeping the key + separators + scheme so the observation stays
// readable and valid JSON.
//
// It runs at the STRING level (not over parsed JSON keys) on purpose:
// bough's observations frequently embed secrets inside command/tool_input
// *values* (e.g. `export API_KEY=AKIA...`), which a key-name walk would
// miss. The class is deliberately loose — for a security scrub, recall
// beats precision (over-redacting a prose word is cheaper than leaking a
// credential), so ECC's pattern is kept as-is including its occasional
// false positive on prose like "token bucket algorithm".
var secretPattern = regexp.MustCompile(
	`(?i)(api[_-]?key|token|secret|password|authorization|credentials?|auth)(["'\s:=]+)([A-Za-z]+\s+)?([A-Za-z0-9_\-/.+=]{8,})`,
)

// scrubSecrets redacts secret tokens in the raw observation bytes. The
// replacement keeps capture groups 1-3 (key, separators, scheme word)
// and substitutes the token with [REDACTED]; the token class excludes
// quotes so a `"key":"secret"` value stays valid JSON after redaction.
func scrubSecrets(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	return secretPattern.ReplaceAll(b, []byte(`${1}${2}${3}[REDACTED]`))
}

// truncateLongStrings walks the observation JSON and truncates any string
// value longer than maxObsFieldChars. The walk is structure-aware (so the
// result is always valid JSON) and only re-marshals when something was
// actually truncated — a payload whose fields are all within the cap is
// returned byte-for-byte unchanged. Best-effort: if the bytes do not
// parse (the handler already validated JSON upstream, but be safe) the
// input is returned untouched.
func truncateLongStrings(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return b
	}
	nv, changed := walkTruncate(v)
	if !changed {
		return b
	}
	out, err := json.Marshal(nv)
	if err != nil {
		return b
	}
	return out
}

func walkTruncate(v any) (any, bool) {
	switch t := v.(type) {
	case string:
		r := []rune(t)
		if len(r) > maxObsFieldChars {
			return string(r[:maxObsFieldChars]) + truncMarker, true
		}
		return t, false
	case map[string]any:
		changed := false
		for k, val := range t {
			nv, c := walkTruncate(val)
			if c {
				t[k] = nv
				changed = true
			}
		}
		return t, changed
	case []any:
		changed := false
		for i, val := range t {
			nv, c := walkTruncate(val)
			if c {
				t[i] = nv
				changed = true
			}
		}
		return t, changed
	default:
		return v, false
	}
}

// sanitizeObservation is the write-time guard applied to every stored
// observation payload: redact secrets first (string-level), then bound
// per-field length (structure-aware). The in-memory payload used for
// quality-gate matching is left untouched — only the persisted copy is
// sanitized.
func sanitizeObservation(b []byte) []byte {
	return truncateLongStrings(scrubSecrets(b))
}
