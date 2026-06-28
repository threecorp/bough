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

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
