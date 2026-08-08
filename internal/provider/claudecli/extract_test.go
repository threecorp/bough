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

// TestExtractResultJSONKeepsAVerdictThatFollowsProse is the case a real
// GATE 5 run produced on 2026-08-08: asked for a verdict, the judge
// reasoned in prose first and put the ```json block after it. The fence
// was not at position 0, so the whole reply reached json.Unmarshal, died
// on the leading 'A', and a PASS verdict became DOUBT — the cluster was
// dropped and the LLM call wasted. Verbatim shape, trimmed.
func TestExtractResultJSONKeepsAVerdictThatFollowsProse(t *testing.T) {
	reply := "All 7 members describe the same procedure — download the *published* " +
		"release asset, extract it, verify the version.\n\n" +
		"```json\n{\"verdict\":\"PASS\",\"confidence\":0.9,\"label\":\"release-asset-verification\"}\n```"
	envelope := []byte(`{"type":"result","result":` + strconv.Quote(reply) + `}`)

	got, err := ExtractResultJSON(envelope)
	if err != nil {
		t.Fatalf("ExtractResultJSON: %v", err)
	}
	var v struct {
		Verdict string  `json:"verdict"`
		Conf    float64 `json:"confidence"`
	}
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("the verdict must survive prose before the fence: %v\ngot: %s", err, got)
	}
	if v.Verdict != "PASS" || v.Conf != 0.9 {
		t.Errorf("verdict = %+v, want PASS/0.9 (got bytes: %s)", v, got)
	}
}

// TestExtractResultJSONKeepsABareObjectAfterProse covers the same shape
// without a fence: some replies just narrate and then emit the object.
func TestExtractResultJSONKeepsABareObjectAfterProse(t *testing.T) {
	reply := "Here is my verdict.\n\n{\"verdict\":\"FAIL\",\"confidence\":0.2}"
	envelope := []byte(`{"type":"result","result":` + strconv.Quote(reply) + `}`)

	got, err := ExtractResultJSON(envelope)
	if err != nil {
		t.Fatalf("ExtractResultJSON: %v", err)
	}
	var v struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("a bare object after prose must survive: %v\ngot: %s", err, got)
	}
	if v.Verdict != "FAIL" {
		t.Errorf("verdict = %q, want FAIL", v.Verdict)
	}
}
