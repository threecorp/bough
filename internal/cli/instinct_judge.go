package cli

import (
	"context"
	"fmt"
	"strings"
	"text/template"

	"github.com/ikeikeikeike/bough/internal/instinctgate"
	"github.com/ikeikeikeike/bough/internal/prompts"
	"github.com/ikeikeikeike/bough/internal/provider/claudecli"
)

// newGateReviewer wires the LLM half of the generation gate. It gets its
// OWN provider and limiter rather than sharing the minting one: the
// judge and the minter compete for the same per-hour ceiling, and a
// session that mints a lot would otherwise exhaust the budget before
// anything got reviewed — the guard would quietly stop running exactly
// when it had the most to check.
//
// Returns nil when the judge cannot be constructed, which the caller
// treats as "layer off" rather than as a failure: the deterministic
// layers already ran, and refusing to mint because a model is
// unreachable would stop the corpus growing over an optional check.
func newGateReviewer(model string, maxCalls int) *instinctgate.Reviewer {
	resolver := prompts.NewResolver()
	tpl, err := resolver.Get(prompts.TemplateInstinctGate)
	if err != nil {
		return nil
	}
	prov := claudecli.NewProvider()
	if model != "" {
		prov.Model = model
	}
	if maxCalls > 0 {
		prov.Limiter.MaxCallsPerSession = maxCalls
	}
	review := func(ctx context.Context, trigger, action string) ([]byte, error) {
		body, rerr := renderGatePrompt(tpl.Body, trigger, action)
		if rerr != nil {
			return nil, rerr
		}
		res, gerr := prov.Generate(ctx, claudecli.GenerateRequest{Template: tpl, Data: gatePromptData{Trigger: trigger, Action: action}})
		if gerr != nil {
			return nil, gerr
		}
		if len(res.Raw) > 0 {
			return res.Raw, nil
		}
		// A provider that returned no raw payload has told us nothing;
		// surfacing it as an error keeps the vote from counting silence
		// as a clean verdict.
		return nil, fmt.Errorf("instinct gate: empty judge response (prompt %d bytes)", len(body))
	}
	return instinctgate.NewReviewer(review)
}

// gatePromptData is the template's view of one candidate: only the
// propagating surface, never the evidence body, so the judge answers the
// same question the deterministic layers do.
type gatePromptData struct {
	Trigger string
	Action  string
}

// renderGatePrompt renders the judge template. Used for the byte count
// in the empty-response error and by the dry-run preview.
func renderGatePrompt(body, trigger, action string) (string, error) {
	tpl, err := template.New("instinct_gate").Parse(body)
	if err != nil {
		return "", fmt.Errorf("instinct gate: parse prompt: %w", err)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, gatePromptData{Trigger: trigger, Action: action}); err != nil {
		return "", fmt.Errorf("instinct gate: render prompt: %w", err)
	}
	return b.String(), nil
}
