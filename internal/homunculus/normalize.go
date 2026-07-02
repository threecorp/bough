package homunculus

import (
	"bytes"
	"strings"
)

// IsCorruptInstinct reports whether raw is an instinct file that was
// serialized as a single physical line carrying literal `\n` escape
// sequences instead of real newlines — the everything-claude-code
// failure mode where the observer model wrote the .md itself via its
// Write/Bash tool and JSON-escaped the content (see the
// bough-instinct-corruption handover).
//
// bough's own observe→write loop cannot produce this (the observer
// returns JSON only and never holds the Write tool; WriteInstinctFile
// emits real newlines). It only arrives across the `bough ecc import`
// boundary from a foreign corpus. Such a file has its `---` frontmatter
// delimiters mid-line, so parseInstinct's split fails and the instinct
// is silently dead. A healthy file has many real newlines and never
// carries the two-char `\n`+`id:` marker, so this stays false for it —
// which also makes NormalizeInstinct idempotent.
func IsCorruptInstinct(raw []byte) bool {
	return bytes.Count(raw, []byte("\n")) <= 2 && bytes.Contains(raw, []byte(`\nid:`))
}

// unescapeInstinct converts the literal escape sequences (`\n`, `\t`,
// `\r`, `\"`, `\'`) back to their real bytes, protecting an already-
// escaped backslash (`\\`) first so a genuine literal backslash in the
// body survives. It deliberately does NOT strip stray bare-`"` lines the
// way the reference Python normalizer did: that could silently delete a
// legitimate first/last body line whose only content is a double-quote,
// and NormalizeInstinct already refuses to write a repair whose
// frontmatter does not re-parse — so a genuinely JSON-string-wrapped
// file is copied verbatim (and surfaces as skipped) rather than
// healed-and-corrupted.
func unescapeInstinct(s string) string {
	s = strings.TrimSuffix(s, "\n")
	const sentinel = "\x00"
	s = strings.ReplaceAll(s, `\\`, sentinel) // protect escaped backslashes first
	s = strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\r`, "\r", `\"`, `"`, `\'`, "'").Replace(s)
	s = strings.ReplaceAll(s, sentinel, `\`)
	return strings.Trim(s, "\n") + "\n"
}

// NormalizeInstinct heals a single-physical-line escaped instinct file
// back into real newlines. wantID is the expected frontmatter id (the
// filename without .md). It returns (repaired, true) only when raw was
// corrupt AND the un-escaped form re-parses through the exact same
// parseInstinct path ReadInstinctFile uses AND its id matches wantID.
// A healthy file, or one whose repair does not validate, returns
// (raw, false) so the caller copies it verbatim — the guard that keeps
// a mis-repair from ever overwriting a real instinct with garbage.
func NormalizeInstinct(raw []byte, wantID string) ([]byte, bool) {
	if !IsCorruptInstinct(raw) {
		return raw, false
	}
	// unescapeInstinct protects `\\` with a NUL sentinel, so a pre-existing
	// NUL byte in the body would be rewritten to a stray backslash. Instinct
	// files are text; a NUL means this is not a normal instinct, so copy it
	// verbatim rather than risk mangling unrelated bytes during the repair.
	if bytes.IndexByte(raw, 0) >= 0 {
		return raw, false
	}
	repaired := []byte(unescapeInstinct(string(raw)))
	in, err := parseInstinct(repaired)
	if err != nil || in.ID != wantID {
		return raw, false
	}
	return repaired, true
}
