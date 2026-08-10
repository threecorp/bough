package claudecli

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"
)

// verdictShape mirrors the fields the GATE 5 judge unmarshals, so the
// tests can assert the extracted bytes parse into real values (the bug
// was that the envelope unmarshalled to all-zero → DOUBT fallback).
type verdictShape struct {
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// realEventArray is the shape `claude -p --output-format json` actually
// emits (captured live): an array of stream events whose result element
// carries the model's reply as a ```json-fenced string.
const realEventArray = `[
  {"type":"system","subtype":"init","session_id":"s"},
  {"type":"assistant","message":{"role":"assistant"}},
  {"type":"rate_limit_event"},
  {"type":"result","subtype":"success","is_error":false,
   "result":"` + "```json" + `\n{\n  \"verdict\": \"FAIL\",\n  \"confidence\": 0.95,\n  \"reason\": \"orthogonal workflows\"\n}\n` + "```" + `"}
]`

func TestExtractResultJSON_RealEventArray(t *testing.T) {
	out, err := ExtractResultJSON([]byte(realEventArray))
	if err != nil {
		t.Fatalf("ExtractResultJSON: %v", err)
	}
	var v verdictShape
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("extracted bytes do not parse as verdict: %v (out=%q)", err, out)
	}
	if v.Verdict != "FAIL" || v.Confidence != 0.95 {
		t.Errorf("verdict = %+v, want FAIL/0.95 (the model's real decision, not DOUBT)", v)
	}
}

func TestExtractResultJSON_SingleEnvelopeObject(t *testing.T) {
	env := `{"type":"result","subtype":"success","result":"` + "```json" +
		`\n{\"verdict\":\"PASS\",\"confidence\":0.8,\"reason\":\"coherent\"}\n` + "```" + `"}`
	out, err := ExtractResultJSON([]byte(env))
	if err != nil {
		t.Fatalf("ExtractResultJSON: %v", err)
	}
	var v verdictShape
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("parse: %v (out=%q)", err, out)
	}
	if v.Verdict != "PASS" {
		t.Errorf("verdict = %q, want PASS", v.Verdict)
	}
}

func TestExtractResultJSON_BareObjectUnchanged(t *testing.T) {
	// FakeExec tests + a future --output-format text hand the judge the
	// answer directly: no "result" wrapper → return as-is.
	bare := `{"verdict":"DOUBT","confidence":0.6,"reason":"finer subdivision"}`
	out, err := ExtractResultJSON([]byte(bare))
	if err != nil {
		t.Fatalf("ExtractResultJSON: %v", err)
	}
	var v verdictShape
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.Verdict != "DOUBT" {
		t.Errorf("verdict = %q, want DOUBT", v.Verdict)
	}
}

func TestExtractResultJSON_ResultStringNoFence(t *testing.T) {
	env := `{"type":"result","result":"{\"verdict\":\"PASS\",\"confidence\":0.7,\"reason\":\"ok\"}"}`
	out, err := ExtractResultJSON([]byte(env))
	if err != nil {
		t.Fatalf("ExtractResultJSON: %v", err)
	}
	var v verdictShape
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("parse: %v (out=%q)", err, out)
	}
	if v.Verdict != "PASS" || v.Confidence != 0.7 {
		t.Errorf("verdict = %+v, want PASS/0.7", v)
	}
}

func TestExtractResultJSON_Empty(t *testing.T) {
	if _, err := ExtractResultJSON([]byte("   ")); !errors.Is(err, ErrEmptyOutput) {
		t.Errorf("empty input err = %v, want ErrEmptyOutput", err)
	}
}

func TestExtractResultJSON_ArrayWithoutResultElement(t *testing.T) {
	// A truncated stream with no terminal result: must error, not
	// silently return garbage that maps to a zero verdict.
	arr := `[{"type":"system"},{"type":"rate_limit_event"}]`
	if _, err := ExtractResultJSON([]byte(arr)); err == nil {
		t.Errorf("expected an error when no result element is present")
	}
}

func TestStripCodeFence(t *testing.T) {
	cases := map[string]string{
		"```json\n{\"a\":1}\n```": `{"a":1}`,
		"```\n{\"a\":1}\n```":     `{"a":1}`,
		`{"a":1}`:                 `{"a":1}`,
		"  {\"a\":1}  ":           `{"a":1}`,
	}
	for in, want := range cases {
		if got := string(stripCodeFence([]byte(in))); got != want {
			t.Errorf("stripCodeFence(%q) = %q, want %q", in, got, want)
		}
	}
}

// resultEnvelope wraps a model reply the way the CLI does, so each case
// below states only the reply it is about.
func resultEnvelope(reply string) []byte {
	return []byte(`{"type":"result","result":` + strconv.Quote(reply) + `}`)
}

// extractVerdict runs the extractor and parses the bytes into the shared
// verdictShape, failing with the raw bytes when either step breaks —
// "which reply shapes survive" is the whole subject of these cases.
func extractVerdict(t *testing.T, reply string) verdictShape {
	t.Helper()
	got, err := ExtractResultJSON(resultEnvelope(reply))
	if err != nil {
		t.Fatalf("ExtractResultJSON: %v", err)
	}
	var v verdictShape
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("the verdict must survive this reply shape: %v\ngot: %s", err, got)
	}
	return v
}

// The shapes a GATE 5 judge actually produces. Every one of these was a
// lost verdict at some point: the reply parsed as prose, json.Unmarshal
// failed, and the cluster fell back to DOUBT with the LLM call wasted.
// The first is verbatim from a real run on 2026-08-08.
func TestExtractResultJSONSurvivesTheJudgesReplyShapes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		want  verdictShape
	}{
		{
			name: "prose then fence",
			reply: "All 7 members describe the same procedure — download the *published* " +
				"release asset, extract it, verify the version.\n\n" +
				"```json\n{\"verdict\":\"PASS\",\"confidence\":0.9}\n```",
			want: verdictShape{Verdict: "PASS", Confidence: 0.9},
		},
		{
			name:  "prose then bare object",
			reply: "Here is my verdict.\n\n{\"verdict\":\"FAIL\",\"confidence\":0.2}",
			want:  verdictShape{Verdict: "FAIL", Confidence: 0.2},
		},
		{
			name:  "bare object then trailing prose",
			reply: "{\"verdict\":\"PASS\",\"confidence\":0.9}\n\nLet me know if you want more detail.",
			want:  verdictShape{Verdict: "PASS", Confidence: 0.9},
		},
		{
			name: "a quoted snippet block before the verdict block",
			reply: "They all run\n\n```bash\nmake test\n```\n\nbefore commit.\n\n" +
				"```json\n{\"verdict\":\"PASS\",\"confidence\":0.9}\n```",
			want: verdictShape{Verdict: "PASS", Confidence: 0.9},
		},
		{
			// Clean JSON that merely mentions a fence inside a string. The
			// extractor must not read those backticks as a block opener.
			name:  "fence inside a string value",
			reply: `{"verdict":"PASS","confidence":0.9,"reason":"members all run ` + "```make test```" + ` first"}`,
			want:  verdictShape{Verdict: "PASS", Confidence: 0.9, Reason: "members all run ```make test``` first"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractVerdict(t, tc.reply); got != tc.want {
				t.Errorf("verdict = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A payload that is a top-level array must come back whole. unquoteResult
// documents that shape as reachable, and slicing brace-to-brace would hand
// the caller `{...},{...}` with the brackets gone.
func TestExtractResultJSONKeepsATopLevelArrayIntact(t *testing.T) {
	got, err := ExtractResultJSON([]byte(`{"type":"result","result":[{"a":1},{"b":2}]}`))
	if err != nil {
		t.Fatalf("ExtractResultJSON: %v", err)
	}
	var arr []map[string]int
	if err := json.Unmarshal(got, &arr); err != nil {
		t.Fatalf("an array payload must survive: %v\ngot: %s", err, got)
	}
	if len(arr) != 2 || arr[0]["a"] != 1 || arr[1]["b"] != 2 {
		t.Errorf("array = %v, want [{a:1} {b:2}] (got bytes: %s)", arr, got)
	}
}
