package instinct

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ikeikeikeike/bough/pkg/schema"
)

// Redactor strips PII / secret patterns from TraceBundle content
// before any minter sees it. v0.5 ships a tiny pattern set (email,
// api_key, token, password, aws_secret); operators can extend it
// via `.bough.yaml`'s `instinct.mint.redaction.pii_patterns`. The
// patterns are merged into a single compiled regex per Redactor
// instance — cheap to apply, costly only at startup.
//
// Memory poisoning is round 1's No. 1 risk; PII / secret leak is
// the round 3 amplifier. Both want a redaction barrier between
// the raw observer output and anything that persists.
type Redactor struct {
	enabled bool
	regex   *regexp.Regexp
}

// Built-in pattern fragments. The keys match the values the config
// validator accepts in `pii_patterns`; the value is a Go regex
// snippet plugged into one alternation group.
var defaultPIIPatterns = map[string]string{
	"email":      `[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`,
	"api_key":    `\bsk-[A-Za-z0-9]{20,}\b`,
	"token":      `\b(?:ghp|github_pat)_[A-Za-z0-9_]{20,}\b`,
	"password":   `(?i)password\s*[:=]\s*\S+`,
	"aws_secret": `\bAKIA[0-9A-Z]{16}\b`,
}

// NewRedactor compiles the merged regex from the names in
// patternNames. Unknown names are reported back to the caller so the
// CLI can warn about typos in `pii_patterns`.
func NewRedactor(enabled bool, patternNames []string) (*Redactor, []error) {
	r := &Redactor{enabled: enabled}
	if !enabled || len(patternNames) == 0 {
		return r, nil
	}
	var snippets []string
	var errs []error
	for _, name := range patternNames {
		snip, ok := defaultPIIPatterns[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			errs = append(errs, fmt.Errorf("unknown pii_pattern %q (built-in: email|api_key|token|password|aws_secret)", name))
			continue
		}
		snippets = append(snippets, snip)
	}
	if len(snippets) == 0 {
		return r, errs
	}
	combined := "(" + strings.Join(snippets, ")|(") + ")"
	re, err := regexp.Compile(combined)
	if err != nil {
		errs = append(errs, fmt.Errorf("compile redaction regex: %w", err))
		return r, errs
	}
	r.regex = re
	return r, errs
}

// Apply replaces every match of the compiled regex with "[REDACTED]".
// If the redactor is disabled or has no patterns the function is a
// no-op pass-through, which keeps callers from having to gate the
// call on `enabled`.
func (r *Redactor) Apply(content string) string {
	if r == nil || !r.enabled || r.regex == nil {
		return content
	}
	return r.regex.ReplaceAllString(content, "[REDACTED]")
}

// Sanitise rewrites a TraceBundle's Content in place. The host's
// observer pipeline calls this before forwarding the bundle to the
// minter so the minter never sees the original text.
func (r *Redactor) Sanitise(b schema.TraceBundle) schema.TraceBundle {
	b.Content = r.Apply(b.Content)
	return b
}

