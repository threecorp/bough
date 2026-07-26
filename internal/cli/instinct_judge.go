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

// judgeCallCeiling bounds the auto-derived judge budget. Sizing the
// budget to the batch is what stops a cap from silently truncating a
// review, but an UNBOUNDED derivation would give up the self-DoS
// protection the limiter exists for: a 200-instinct mint would spawn 600
// subprocesses against the operator's interactive session.
//
// 30 = 10 candidates x 3 votes, which matches the per-hour ceiling and
// covers an ordinary mint. Past it the operator is told the number and
// can raise it explicitly with --judge-max-calls; the point is that the
// truncation is never silent, not that it can never happen.
const judgeCallCeiling = 30

// newGateReviewer wires the LLM half of the generation gate. It gets its
// OWN provider and limiter rather than sharing the minting one: the judge
// and the minter would otherwise compete for the same ceiling, and a
// session that mints a lot would exhaust the budget before anything got
// reviewed — the guard would stop running exactly when it had most to do.
//
// budget is the per-session call cap for that limiter; 0 uses the
// provider default.
//
// A nil reviewer means "layer off" rather than a hard failure: the
// deterministic layers already ran, and refusing to mint because a model
// is unreachable would stop the corpus growing over an optional check.
// But the REASON is returned so the caller can say so — a pass where the
// judge never ran must not print what a pass that judged everything and
// found nothing prints.
func newGateReviewer(model string, budget int) (*instinctgate.Reviewer, *claudecli.Provider, error) {
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
	if budget > 0 {
		prov.Limiter.MaxCallsPerSession = budget
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
