package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScrubSecrets(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		mustHave  []string // substrings that must survive
		mustNotHv []string // secret tokens that must be gone
	}{
		{
			name:      "env var assignment in a command value",
			in:        `{"tool_input":{"command":"export API_KEY=AKIA1234567890ABCDEF && run"}}`,
			mustHave:  []string{"[REDACTED]", "export API_KEY", "&& run"},
			mustNotHv: []string{"AKIA1234567890ABCDEF"},
		},
		{
			name:      "bearer authorization header",
			in:        `{"authorization":"Bearer eyJ0eXAiOiJKV1QiLCJhbG"}`,
			mustHave:  []string{"[REDACTED]", "Bearer"},
			mustNotHv: []string{"eyJ0eXAiOiJKV1QiLCJhbG"},
		},
		{
			name:      "password json field",
			in:        `{"password":"hunter2supersecret"}`,
			mustHave:  []string{"[REDACTED]"},
			mustNotHv: []string{"hunter2supersecret"},
		},
		{
			name:      "short non-secret value left alone",
			in:        `{"token":"ab"}`, // < 8 token chars → not redacted
			mustHave:  []string{`"token":"ab"`},
			mustNotHv: []string{"[REDACTED]"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := string(scrubSecrets([]byte(tc.in)))
			for _, s := range tc.mustHave {
				if !strings.Contains(out, s) {
					t.Errorf("scrub dropped %q\n  in:  %s\n  out: %s", s, tc.in, out)
				}
			}
			for _, s := range tc.mustNotHv {
				if strings.Contains(out, s) {
					t.Errorf("scrub leaked %q\n  out: %s", s, out)
				}
			}
			// redaction must keep the bytes valid JSON
			if !json.Valid([]byte(out)) {
				t.Errorf("scrub produced invalid JSON: %s", out)
			}
		})
	}
}

func TestTruncateLongStrings(t *testing.T) {
	long := strings.Repeat("x", maxObsFieldChars+500)
	in := `{"tool_response":{"stdout":"` + long + `"},"short":"ok"}`
	out := truncateLongStrings([]byte(in))

	if !json.Valid(out) {
		t.Fatalf("truncate produced invalid JSON")
	}
	var parsed struct {
		ToolResponse struct {
			Stdout string `json:"stdout"`
		} `json:"tool_response"`
		Short string `json:"short"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal truncated: %v", err)
	}
	if !strings.HasSuffix(parsed.ToolResponse.Stdout, truncMarker) {
		t.Errorf("long field not marked truncated: ...%s", tail(parsed.ToolResponse.Stdout, 20))
	}
	if len([]rune(parsed.ToolResponse.Stdout)) != maxObsFieldChars+len([]rune(truncMarker)) {
		t.Errorf("truncated length = %d, want %d", len([]rune(parsed.ToolResponse.Stdout)), maxObsFieldChars+len([]rune(truncMarker)))
	}
	if parsed.Short != "ok" {
		t.Errorf("short field disturbed: %q", parsed.Short)
	}
}

func TestTruncateLongStrings_UnchangedReturnsSameBytes(t *testing.T) {
	in := []byte(`{"a":"short","b":["x","y"],"n":3}`)
	out := truncateLongStrings(in)
	if string(out) != string(in) {
		t.Errorf("under-cap payload was rewritten:\n  in:  %s\n  out: %s", in, out)
	}
}

func TestSanitizeObservation_EmptyAndCombined(t *testing.T) {
	if got := sanitizeObservation(nil); len(got) != 0 {
		t.Errorf("sanitize(nil) = %q, want empty", got)
	}
	// secret inside an over-long field: both scrubbed and valid JSON
	long := strings.Repeat("y", maxObsFieldChars+10)
	in := `{"cmd":"token=ABCDEF1234567890 ` + long + `"}`
	out := sanitizeObservation([]byte(in))
	if !json.Valid(out) {
		t.Fatalf("sanitize produced invalid JSON: %s", out)
	}
	if strings.Contains(string(out), "ABCDEF1234567890") {
		t.Errorf("sanitize leaked secret: %s", out)
	}
}

// TestSanitizeObservation_NeverDropsValidPayload is the v0.9.18 regression for
// the data-loss bug: scrubbing a secret-keyed UNQUOTED scalar used to yield
// invalid JSON (`{"token":12345678}` → `{"token":[REDACTED]}`), which
// json.Marshal(record) then rejected — silently dropping the whole
// observation. sanitizeObservation must always return valid JSON for a valid
// input so the record is never lost.
func TestSanitizeObservation_NeverDropsValidPayload(t *testing.T) {
	cases := []string{
		`{"token":12345678}`,                          // unquoted numeric secret (the repro)
		`{"tool_input":{"api_key":98765432101234}}`,   // nested unquoted numeric
		`{"password":true,"token":12345678}`,          // mixed scalar types
		`{"authorization":"Bearer eyJ0eXAiOiJKV1Qi"}`, // ordinary quoted secret stays valid
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if !json.Valid([]byte(in)) {
				t.Fatalf("test input itself is not valid JSON: %s", in)
			}
			out := sanitizeObservation([]byte(in))
			if !json.Valid(out) {
				t.Fatalf("sanitize turned a valid payload into invalid JSON (record would be dropped):\n  in:  %s\n  out: %s", in, out)
			}
		})
	}
}

// TestSanitizeObservation_NoSiblingLeakOnFallback is the v0.9.18 self-review
// fix: when an unquoted secret-keyed scalar forces the structured fallback, a
// SIBLING string secret must still be redacted. The first guard fell back to
// the whole raw payload, persisting the sibling secret in clear.
func TestSanitizeObservation_NoSiblingLeakOnFallback(t *testing.T) {
	in := `{"token":12345678,"api_key":"AKIAVALIDSECRET123"}`
	out := sanitizeObservation([]byte(in))
	if !json.Valid(out) {
		t.Fatalf("sanitize produced invalid JSON: %s", out)
	}
	if strings.Contains(string(out), "AKIAVALIDSECRET123") {
		t.Errorf("sibling secret leaked on the structured fallback: %s", out)
	}
}

// TestScrubSecrets_EscapedJSONStringValue is the v0.9.18 regression for the
// recall gap: a secret inside an ESCAPED JSON string value (a tool whose
// stdout is itself a JSON body) used to slip past redaction because the `\"`
// between key and value broke the contiguous separator match.
func TestScrubSecrets_EscapedJSONStringValue(t *testing.T) {
	in := `{"tool_response":{"stdout":"{\"access_token\":\"AKIA1234567890SECRET\"}"}}`
	out := string(scrubSecrets([]byte(in)))
	if strings.Contains(out, "AKIA1234567890SECRET") {
		t.Errorf("scrub leaked a secret in an escaped-JSON string value:\n  out: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("scrub did not redact the escaped-JSON secret:\n  out: %s", out)
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("scrub produced invalid JSON: %s", out)
	}
}

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
