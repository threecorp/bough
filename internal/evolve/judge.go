package evolve

import (
	"context"
	"fmt"
	"strings"
)

// Verdict is the GATE 5 LLM decision. It mirrors the JSON schema the
// evolve_judge prompt enforces.
type Verdict struct {
	Decision        string  `json:"verdict"` // PASS | DOUBT | FAIL
	Confidence      float64 `json:"confidence"`
	Label           string  `json:"label"`
	Description     string  `json:"description"`
	Reason          string  `json:"reason"`
	ReusePriorLabel bool    `json:"reuse_prior_label"`
}

// Verdict decision constants.
const (
	DecisionPass  = "PASS"
	DecisionDoubt = "DOUBT"
	DecisionFail  = "FAIL"
)

// Judge is the GATE 5 backend. The pipeline passes a cluster + its
// gate metrics; the implementation returns a Verdict. Production wires
// a claudecli-backed Judge; tests inject a deterministic stub so the
// 5-gate flow can be exercised without spawning `claude --print`.
type Judge interface {
	Judge(ctx context.Context, in JudgeInput) (Verdict, error)
}

// JudgeInput carries everything the judge prompt template renders.
// The numeric gate metrics are passed through verbatim so the LLM
// sees the same numbers the mechanical gates computed.
type JudgeInput struct {
	ProjectName     string
	MemberCount     int
	Cohesion        float64
	LexiconCoverage float64
	RelIsolation    float64
	MaxPriorOverlap float64
	NearestPriorLabel string
	Cluster         Cluster
	Gate            GateVerdict
}

// JudgeData is the flat struct the evolve_judge.md template binds
// against. BuildJudgeData renders the member block + formats the
// metric numbers so the template stays logic-free.
type JudgeData struct {
	ProjectName       string
	MemberCount       int
	Cohesion          string
	LexiconCoverage   string
	RelIsolation      string
	MaxPriorOverlap   string
	NearestPriorLabel string
	Members           string
}

// BuildJudgeData projects a JudgeInput into the template data. The
// member block is one "N. id\n   trigger: ...\n   action: ..." entry
// per member so the LLM reads each member's surface without us
// dumping the raw frontmatter.
func BuildJudgeData(in JudgeInput) JudgeData {
	var b strings.Builder
	for i, m := range in.Cluster.Members {
		fmt.Fprintf(&b, "%d. %s\n", i+1, m.ID)
		fmt.Fprintf(&b, "   trigger: %s\n", oneLine(m.Trigger))
		fmt.Fprintf(&b, "   action:  %s\n", firstActionLine(m.Body))
	}
	nearest := in.NearestPriorLabel
	if nearest == "" {
		nearest = "(none — first evolve pass for this project)"
	}
	return JudgeData{
		ProjectName:       in.ProjectName,
		MemberCount:       in.MemberCount,
		Cohesion:          fmt.Sprintf("%.3f", in.Cohesion),
		LexiconCoverage:   fmt.Sprintf("%.3f", in.LexiconCoverage),
		RelIsolation:      fmt.Sprintf("%.3f", in.RelIsolation),
		MaxPriorOverlap:   fmt.Sprintf("%.3f", in.MaxPriorOverlap),
		NearestPriorLabel: nearest,
		Members:           strings.TrimRight(b.String(), "\n"),
	}
}

// ValidateVerdict enforces the schema invariants the pipeline relies
// on before acting on a verdict: a known decision literal, confidence
// in range, and a PASS that actually carries a kebab-case label.
func ValidateVerdict(v Verdict) error {
	switch v.Decision {
	case DecisionPass, DecisionDoubt, DecisionFail:
	default:
		return fmt.Errorf("evolve.ValidateVerdict: unknown verdict %q", v.Decision)
	}
	if v.Confidence < 0 || v.Confidence > 1 {
		return fmt.Errorf("evolve.ValidateVerdict: confidence %v out of [0,1]", v.Confidence)
	}
	if v.Decision == DecisionPass {
		if !labelPattern.MatchString(v.Label) {
			return fmt.Errorf("evolve.ValidateVerdict: PASS verdict needs a kebab-case label, got %q", v.Label)
		}
	}
	return nil
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// firstActionLine extracts the first non-empty line under "## Action"
// from an instinct body. Falls back to the first non-heading line so
// older instinct shapes still surface something useful.
func firstActionLine(body string) string {
	lines := strings.Split(body, "\n")
	inAction := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.EqualFold(t, "## Action") {
			inAction = true
			continue
		}
		if inAction {
			if t == "" {
				continue
			}
			if strings.HasPrefix(t, "## ") {
				break
			}
			return oneLine(t)
		}
	}
	// fallback: first non-heading non-empty line
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		return oneLine(t)
	}
	return "(no action)"
}
