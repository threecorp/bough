package cli

import (
	"context"
	"encoding/json"
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
// A nil reviewer means "layer off" rather than a hard failure: the
// deterministic layers already ran, and refusing to mint because a model
// is unreachable would stop the corpus growing over an optional check.
// But the REASON is returned so the caller can say so — a pass where the
// judge never ran must not print the same thing as a pass where it ran
// and found nothing.
//
// The provider is returned alongside the reviewer so the caller can print
// ITS limiter snapshot too. Because this budget is separate from the
// minting one, a run with the judge on spends against two caps; reporting
// only the minting one would understate the pass's real cost, and a cap
// nobody can see is the silent kind.
func newGateReviewer(model string, maxCalls int) (*instinctgate.Reviewer, *claudecli.Provider, error) {
	resolver := prompts.NewResolver()
	tpl, err := resolver.Get(prompts.TemplateInstinctGate)
	if err != nil {
		return nil, nil, fmt.Errorf("instinct gate: prompt template unavailable: %w", err)
	}
	// Parse the template ONCE, here, rather than discovering a malformed
	// override inside the first vote. Rendering happens inside Generate,
	// which acquires a limiter slot and records a failure before it ever
	// reaches the template — so a prompt that does not parse used to burn
	// 3 judge slots per candidate and could trip the circuit breaker,
	// reported as if the model were down.
	if _, rerr := renderGatePrompt(tpl.Body, "probe", "probe"); rerr != nil {
		return nil, nil, rerr
	}
	prov := claudecli.NewProvider()
	if model != "" {
		prov.Model = model
	}
	if maxCalls > 0 {
		prov.Limiter.MaxCallsPerSession = maxCalls
	}
	review := func(ctx context.Context, trigger, action string) ([]byte, error) {
		// The template is rendered by Generate. Rendering it here as well
		// would double the work on every vote (3 per candidate) for a byte
		// count only the empty-response error ever reads, so that render is
		// deferred to the error path below.
		res, gerr := prov.Generate(ctx, claudecli.GenerateRequest{Template: tpl, Data: gatePromptData{Trigger: trigger, Action: action}})
		if gerr != nil {
			return nil, gerr
		}
		// Return the UNWRAPPED document, not res.Raw. Raw is the CLI's
		// envelope ({"type":"result", …, "result":"```json…```"}); the
		// verdict lives inside it, and the provider has already unwrapped
		// it into Parsed. Handing the envelope to the verdict decoder
		// parses fine — JSON ignores unknown fields — and yields
		// violation=false, so the judge silently clears everything while
		// reporting a full review. That is the failure this layer exists
		// to prevent, so it must not be how the layer itself fails.
		if len(res.Parsed) > 0 {
			return json.Marshal(res.Parsed)
		}
		// A provider that returned no usable document has told us nothing;
		// surfacing it as an error keeps the vote from counting silence
		// as a clean verdict. The prompt size is rendered only here, on the
		// path that reports it — and a render failure ANNOTATES that error
		// rather than replacing it: the empty response is the finding, and
		// swapping in the render error would hide the very condition this
		// branch exists to report.
		body, rerr := renderGatePrompt(tpl.Body, trigger, action)
		size := fmt.Sprintf("%d bytes", len(body))
		if rerr != nil {
			size = "size unavailable: " + rerr.Error()
		}
		return nil, fmt.Errorf("instinct gate: empty judge response (prompt %s)", size)
	}
	return instinctgate.NewReviewer(review), prov, nil
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
