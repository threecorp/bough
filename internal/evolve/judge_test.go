package evolve

import (
	"context"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

func TestBuildJudgeData_RendersMembers(t *testing.T) {
	c := Cluster{Members: []*homunculus.Instinct{
		{ID: "io-a", Trigger: "when wrapping io", Body: "## Action\nWrap I/O in the data layer.\n## Evidence\n- x"},
		{ID: "io-b", Trigger: "when wrapping io again", Body: "## Action\nKeep transport in data layer.\n"},
	}}
	in := JudgeInput{
		ProjectName: "demo",
		MemberCount: 2,
		Cohesion:    0.42,
		Cluster:     c,
	}
	data := BuildJudgeData(in)
	if data.MemberCount != 2 {
		t.Errorf("MemberCount = %d", data.MemberCount)
	}
	if data.Cohesion != "0.420" {
		t.Errorf("Cohesion = %q, want 0.420", data.Cohesion)
	}
	if !strings.Contains(data.Members, "1. io-a") || !strings.Contains(data.Members, "2. io-b") {
		t.Errorf("member block missing entries:\n%s", data.Members)
	}
	if !strings.Contains(data.Members, "Wrap I/O in the data layer.") {
		t.Errorf("action line not extracted:\n%s", data.Members)
	}
	if data.NearestPriorLabel == "" {
		t.Errorf("NearestPriorLabel should fall back to a placeholder")
	}
}

func TestValidateVerdict(t *testing.T) {
	cases := []struct {
		name    string
		v       Verdict
		wantErr bool
	}{
		{"valid PASS", Verdict{Decision: "PASS", Confidence: 0.8, Label: "io-data-layer"}, false},
		{"valid FAIL no label", Verdict{Decision: "FAIL", Confidence: 0.9}, false},
		{"valid DOUBT", Verdict{Decision: "DOUBT", Confidence: 0.5, Label: "prior-label"}, false},
		{"unknown decision", Verdict{Decision: "MAYBE", Confidence: 0.5}, true},
		{"confidence over 1", Verdict{Decision: "PASS", Confidence: 1.5, Label: "x"}, true},
		{"PASS without label", Verdict{Decision: "PASS", Confidence: 0.8}, true},
		{"PASS bad label", Verdict{Decision: "PASS", Confidence: 0.8, Label: "Bad Label"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateVerdict(tc.v)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %t", err, tc.wantErr)
			}
		})
	}
}

func TestClaudeJudge_ParsesVerdict(t *testing.T) {
	render := func(data JudgeData) (string, string, error) {
		return "rendered:" + data.ProjectName, "promptv1", nil
	}
	generate := func(ctx context.Context, body string) ([]byte, error) {
		if !strings.Contains(body, "rendered:demo") {
			t.Errorf("generate got unexpected body: %q", body)
		}
		return []byte(`{"verdict":"PASS","confidence":0.85,"label":"io-data-layer","description":"Apply when wrapping I/O","reason":"coherent workflow"}`), nil
	}
	j := NewClaudeJudge(render, generate)
	v, err := j.Judge(context.Background(), JudgeInput{ProjectName: "demo", MemberCount: 3})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if v.Decision != "PASS" || v.Label != "io-data-layer" {
		t.Errorf("verdict mismatch: %+v", v)
	}
}

func TestClaudeJudge_RejectsSchemaViolation(t *testing.T) {
	render := func(data JudgeData) (string, string, error) { return "x", "v", nil }
	generate := func(ctx context.Context, body string) ([]byte, error) {
		// PASS but no label → ValidateVerdict rejects
		return []byte(`{"verdict":"PASS","confidence":0.85,"reason":"r"}`), nil
	}
	j := NewClaudeJudge(render, generate)
	_, err := j.Judge(context.Background(), JudgeInput{ProjectName: "demo"})
	if err == nil {
		t.Errorf("expected schema validation error for PASS without label")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"IO Lives In Data Layer":   "io-lives-in-data-layer",
		"already-kebab":            "already-kebab",
		"  weird__chars!! here  ":  "weird-chars-here",
		"trailing-":                "trailing",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsValidLabel(t *testing.T) {
	valid := []string{"io-data-layer", "abc", "a1-b2-c3"}
	invalid := []string{"Bad", "with space", "trailing-", "double--dash", ""}
	for _, s := range valid {
		if !IsValidLabel(s) {
			t.Errorf("IsValidLabel(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if IsValidLabel(s) {
			t.Errorf("IsValidLabel(%q) = true, want false", s)
		}
	}
}
