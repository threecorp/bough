package homunculus

import (
	"strings"
	"testing"
)

// corrupt instincts are one physical line where every newline is the
// literal two-char `\n`. A Go backtick literal keeps `\n` as
// backslash-n, so these fixtures mirror the on-disk corruption exactly.

func TestNormalizeInstinct_healsSingleLine(t *testing.T) {
	corrupt := []byte(`---\nid: never-merge-without-authorization\nconfidence: 0.9\ndomain: workflow\n---\n\n## Action\nNever merge a PR without explicit authorization.\n`)
	if !IsCorruptInstinct(corrupt) {
		t.Fatalf("IsCorruptInstinct = false, want true for a single-line escaped file")
	}
	out, repaired := NormalizeInstinct(corrupt, "never-merge-without-authorization")
	if !repaired {
		t.Fatalf("NormalizeInstinct repaired = false, want true")
	}
	if strings.Contains(string(out), `\n`) {
		t.Errorf("repaired form still carries literal \\n:\n%s", out)
	}
	in, err := parseInstinct(out)
	if err != nil {
		t.Fatalf("repaired form does not re-parse: %v\n%s", err, out)
	}
	if in.ID != "never-merge-without-authorization" {
		t.Errorf("repaired id = %q, want never-merge-without-authorization", in.ID)
	}
	if !strings.Contains(in.Body, "Never merge a PR without explicit authorization.") {
		t.Errorf("repaired body lost its content:\n%s", in.Body)
	}
}

func TestNormalizeInstinct_healthyIsNoOp(t *testing.T) {
	healthy := []byte("---\nid: foo\nconfidence: 0.5\n---\n\n## Action\nDo the thing.\n")
	if IsCorruptInstinct(healthy) {
		t.Errorf("IsCorruptInstinct = true for a healthy multi-line file")
	}
	out, repaired := NormalizeInstinct(healthy, "foo")
	if repaired {
		t.Errorf("healthy file reported repaired=true")
	}
	if string(out) != string(healthy) {
		t.Errorf("healthy file was mutated:\n%s", out)
	}
}

func TestNormalizeInstinct_idMismatchLeftVerbatim(t *testing.T) {
	// Corrupt, but the un-escaped id ("foo") does not match the filename
	// wantID ("bar"); the guard must refuse to overwrite bar.md with it.
	corrupt := []byte(`---\nid: foo\n---\n\nbody\n`)
	out, repaired := NormalizeInstinct(corrupt, "bar")
	if repaired {
		t.Errorf("id mismatch reported repaired=true (would mis-key the file)")
	}
	if string(out) != string(corrupt) {
		t.Errorf("mis-keyed file was mutated instead of left verbatim")
	}
}

func TestNormalizeInstinct_idempotent(t *testing.T) {
	corrupt := []byte(`---\nid: foo\n---\n\nbody\n`)
	once, first := NormalizeInstinct(corrupt, "foo")
	if !first {
		t.Fatalf("first pass did not repair")
	}
	twice, again := NormalizeInstinct(once, "foo")
	if again {
		t.Errorf("second pass reported repaired=true (not idempotent)")
	}
	if string(twice) != string(once) {
		t.Errorf("second pass mutated an already-healthy file")
	}
}

func TestUnescapeInstinct_protectsLiteralBackslash(t *testing.T) {
	// A genuine literal backslash-n in the body (written `\\n` in the
	// escaped form) must survive as backslash-n, not collapse to a
	// newline; the real `\n` separators must still un-escape.
	got := unescapeInstinct(`line1\nregex: \\n matches newline\n`)
	if !strings.Contains(got, `\n matches newline`) {
		t.Errorf("literal backslash-n not preserved:\n%q", got)
	}
	if !strings.Contains(got, "line1\n") {
		t.Errorf("real separators not un-escaped:\n%q", got)
	}
}

func TestUnescapeInstinct_keepsBareQuoteBodyLine(t *testing.T) {
	// Regression: the old normalizer stripped any leading/trailing bare
	// `"` line as a JSON wrapper, silently deleting a legitimate body
	// line whose only content is a double-quote. It must survive now.
	got := unescapeInstinct(`---\nid: foo\n---\n\n"\nquoted\n`)
	if !strings.Contains(got, "\n\"\n") {
		t.Errorf("a bare-quote body line was dropped:\n%q", got)
	}
}
