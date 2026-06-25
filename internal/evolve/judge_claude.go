package evolve

import (
	"context"
	"encoding/json"
	"fmt"
)

// GenerateFunc is the minimal surface the evolve package needs from a
// structured-output LLM provider. internal/provider/claudecli's
// Provider.Generate satisfies a thin adapter over this signature;
// keeping the dependency inverted (evolve declares the func type, the
// CLI wires claudecli) means the evolve package has no import edge to
// claudecli and stays unit-testable with a plain closure.
//
// promptBody is the fully-rendered judge prompt; the implementation
// spawns the LLM and returns the raw structured-output bytes (= a JSON
// document). schemaName is advisory metadata for the audit log.
type GenerateFunc func(ctx context.Context, promptBody string) (raw []byte, err error)

// RenderFunc renders the evolve_judge template against JudgeData. The
// CLI wires internal/prompts here so evolve does not import the embed
// FS directly.
type RenderFunc func(data JudgeData) (promptBody string, promptVersion string, err error)

// ClaudeJudge is the production GATE 5 backend. It renders the judge
// prompt, calls the structured-output provider, and unmarshals the
// verdict. Construct via NewClaudeJudge from the CLI layer.
type ClaudeJudge struct {
	render   RenderFunc
	generate GenerateFunc
}

// NewClaudeJudge wires a render + generate pair into a Judge.
func NewClaudeJudge(render RenderFunc, generate GenerateFunc) *ClaudeJudge {
	return &ClaudeJudge{render: render, generate: generate}
}

// Judge renders the prompt, calls the provider, parses + validates the
// verdict. A parse failure or schema violation is returned as an error
// so the pipeline treats it as DOUBT (= the operator reviews rather
// than the cluster silently vanishing).
func (j *ClaudeJudge) Judge(ctx context.Context, in JudgeInput) (Verdict, error) {
	data := BuildJudgeData(in)
	body, _, err := j.render(data)
	if err != nil {
		return Verdict{}, fmt.Errorf("evolve.ClaudeJudge: render: %w", err)
	}
	raw, err := j.generate(ctx, body)
	if err != nil {
		return Verdict{}, fmt.Errorf("evolve.ClaudeJudge: generate: %w", err)
	}
	var v Verdict
	if err := json.Unmarshal(raw, &v); err != nil {
		return Verdict{}, fmt.Errorf("evolve.ClaudeJudge: parse verdict: %w (raw=%q)", err, raw)
	}
	if err := ValidateVerdict(v); err != nil {
		return Verdict{}, err
	}
	return v, nil
}
