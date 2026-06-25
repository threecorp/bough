package evolve

import (
	"regexp"
	"strings"
)

// labelPattern is the canonical kebab-case slug for cluster labels +
// skill / agent / command names. A label that does not match is
// rejected at the GATE 5 boundary so a malformed slug never reaches
// the SKILL.md / symlink layer (= the ECC v3.2 filename ↔ id class
// of bug).
var labelPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Slugify coerces an arbitrary string into a kebab-case slug: lower-
// case, non-alphanumeric runs collapsed to single hyphens, leading /
// trailing hyphens trimmed. Used as a fallback when the LLM returns a
// non-conforming label and the operator passed --force, and to derive
// command slugs from instinct ids.
func Slugify(s string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	return out
}

// IsValidLabel reports whether s is already a conforming kebab-case
// slug (= no Slugify pass needed).
func IsValidLabel(s string) bool {
	return labelPattern.MatchString(s)
}
