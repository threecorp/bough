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
//
// The separator class includes a backslash so a secret living inside an
// *escaped* JSON string value — a tool whose stdout is itself a JSON body,
// e.g. `{"stdout":"{\"access_token\":\"AKIA…\"}"}` — is still redacted; the
// `\"` between key and value would otherwise break the contiguous match
// and leak the credential (a recall gap over ECC's original class).
var secretPattern = regexp.MustCompile(
	`(?i)(api[_-]?key|token|secret|password|authorization|credentials?|auth)(["'\s:=\\]+)([A-Za-z]+\s+)?([A-Za-z0-9_\-/.+=]{8,})`,
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
//
// Data-loss guard: the string-level redaction keeps quoted `"key":"secret"`
// values valid JSON (the token class excludes the closing quote), but an
// *unquoted* scalar under a secret-named key — `{"token":12345678}` →
// `{"token":[REDACTED]}` — becomes INVALID JSON. The caller hands the result
// to json.Marshal as a RawMessage, which would reject it and silently drop
// the entire observation, starving the corpus.
//
// When that happens we must NOT fall back to the raw payload — that would
// persist any *sibling* secrets (which scrubbing redacted perfectly in
// isolation) in clear. Instead redact structurally over the parsed JSON
// (structuredScrub): every secret-named key's value becomes "[REDACTED]" and
// every string value is re-scrubbed, which is valid by construction and never
// leaks a cleanly-redactable secret. The structured path slightly over-redacts
// (it drops a scheme word like "Bearer"), but it only runs on the rare
// unquoted-scalar payload; the common path keeps the precise byte-level scrub.
func sanitizeObservation(b []byte) []byte {
	scrubbed := truncateLongStrings(scrubSecrets(b))
	if len(b) > 0 && json.Valid(b) && !json.Valid(scrubbed) {
		if repaired, ok := structuredScrub(b); ok {
			return truncateLongStrings(repaired)
		}
		return truncateLongStrings(b) // unreachable for valid b; keep the record
	}
	return scrubbed
}

// secretKeyRE matches a JSON KEY that names a secret — the same vocabulary as
// secretPattern, anchored as a whole key.
var secretKeyRE = regexp.MustCompile(`(?i)^(api[_-]?key|token|secret|password|authorization|credentials?|auth)$`)

// structuredScrub redacts secrets over the PARSED JSON so the result is valid
// by construction: any value under a secret-named key becomes "[REDACTED]", and
// every string value is run through the byte-level scrub (json.Marshal then
// re-quotes it safely). Used only as the sanitizeObservation fallback when the
// byte-level scrub broke an otherwise-valid payload. ok=false if b is not JSON.
func structuredScrub(b []byte) ([]byte, bool) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, false
	}
	out, err := json.Marshal(walkScrub(v))
	if err != nil {
		return nil, false
	}
	return out, true
}

func walkScrub(v any) any {
	switch t := v.(type) {
	case string:
		return string(scrubSecrets([]byte(t)))
	case map[string]any:
		for k, val := range t {
			if secretKeyRE.MatchString(k) {
				t[k] = "[REDACTED]"
			} else {
				t[k] = walkScrub(val)
			}
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = walkScrub(val)
		}
		return t
	default:
		return v
	}
}
