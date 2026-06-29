package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/observe"
	"github.com/ikeikeikeike/bough/internal/prompts"
	"github.com/ikeikeikeike/bough/internal/provider/claudecli"
)

// newObserverCmd wires the `bough observer` namespace. v0.9.0 ships
// `run-once` so an operator can fire one extraction pass
// synchronously (= called by SessionEnd / WorktreeRemove hooks).
// The opt-in daemon (`bough observer start/stop/status`) lands in
// v0.9.2.
func newObserverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "observer",
		Short: "Observe Claude Code session events and extract instincts",
		Long: `bough observer is the continuous-learning surface that turns
captured tool-use observations into Claude-generated instinct files
under ~/.local/share/bough-homunculus/.

Every call goes through ` + "`claude --print`" + ` so the LLM cost stays
inside the operator's Claude Code subscription. v0.9 hard-caps the
number of calls per session and per hour to protect the operator's
interactive session from a runaway observer.`,
	}
	cmd.AddCommand(
		newObserverRunOnceCmd(),
		newObserverStartCmd(),
		newObserverStopCmd(),
		newObserverStatusCmd(),
		newObserverRunDaemonCmd(),
	)
	return cmd
}

const (
	observerDefaultWindowSize      = 200
	observerDefaultExistingPreview = 50
)

// newObserverRunOnceCmd is the synchronous single-shot extraction
// pass. Designed to be fired by a hook (SessionEnd / WorktreeRemove
// / explicit operator call) — it reads the tail of the per-project
// observations.jsonl, asks Claude to extract instincts, validates
// the structured JSON, and writes one .md per accepted instinct.
func newObserverRunOnceCmd() *cobra.Command {
	var (
		root       string
		windowSize int
		dryRun     bool
		out        string
		model      string
		maxCalls   int
	)
	cmd := &cobra.Command{
		Use:   "run-once",
		Short: "Read recent observations, ask Claude for instincts, write the results to homunculus",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			cwd := root
			if cwd == "" {
				w, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("observer run-once: getwd: %w", err)
				}
				cwd = w
			}
			// Resolve identity from the MONOREPO ROOT, not raw cwd, so run-once
			// reads the same archive the hook + observer daemon pool into
			// (DetectIdentity(resolveMonorepoRoot(cwd))). Run from a sub-repo /
			// worktree with the raw cwd, the id would diverge from the writer's
			// and run-once would mint from an empty, un-pooled project.
			root := resolveMonorepoRoot(cwd)
			ident, err := homunculus.DetectIdentity(root)
			if err != nil {
				return err
			}
			layout := homunculus.NewLayout()
			if err := layout.EnsureGlobalDirs(); err != nil {
				return err
			}
			if err := layout.EnsureProjectDirs(ident.ID); err != nil {
				return err
			}
			reg := homunculus.NewRegistryRW(layout)
			if err := reg.WriteUpsert(homunculus.Project{
				ID:     ident.ID,
				Name:   ident.Name,
				Root:   ident.Root,
				Remote: ident.Remote,
			}); err != nil {
				return err
			}

			// Read the homunculus archive — where `bough hook handle` appends
			// since v0.9.10 and `bough ecc import` writes — and ALSO a legacy
			// hook inbox at <root>/.bough/observations.jsonl for back-compat:
			// pre-v0.9.10 the hook wrote there, so a stale inbox may still hold
			// un-minted observations. It is no longer written; TailNMerged
			// skips it when absent (the normal case post-v0.9.10).
			archivePath := layout.ObservationsFile(ident.ID)
			inboxPath := filepath.Join(root, ".bough", "observations.jsonl")
			observations, err := observe.TailNMerged(windowSize, archivePath, inboxPath)
			if err != nil {
				return fmt.Errorf("observer run-once: read observations: %w", err)
			}
			stdout := cmd.OutOrStdout()
			if len(observations) == 0 {
				fmt.Fprintf(stdout, "no observations under %s or %s — nothing to extract\n", inboxPath, archivePath)
				return nil
			}

			existing, _ := homunculus.ScanInstincts(layout.InstinctsDir(ident.ID))

			resolver := prompts.NewResolver()
			tpl, err := resolver.Get(prompts.TemplateObserver)
			if err != nil {
				return err
			}

			data := buildObserverData(ident, observations, existing, tpl)

			prov := claudecli.NewProvider()
			if model != "" {
				prov.Model = model
			}
			if maxCalls > 0 {
				prov.Limiter.MaxCallsPerSession = maxCalls
			}

			if dryRun {
				body, rerr := renderForPreview(tpl.Body, data)
				if rerr != nil {
					return rerr
				}
				if out != "" {
					if werr := os.WriteFile(out, []byte(body), 0o644); werr != nil {
						return fmt.Errorf("observer run-once: write %s: %w", out, werr)
					}
					fmt.Fprintf(stdout, "wrote rendered prompt to %s (dry-run: no claude --print spawned)\n", out)
					return nil
				}
				fmt.Fprintln(stdout, "--- rendered prompt (dry-run; no claude --print spawned) ---")
				fmt.Fprintln(stdout, body)
				return nil
			}

			res, err := prov.Generate(ctx, claudecli.GenerateRequest{Template: tpl, Data: data})
			if err != nil {
				return fmt.Errorf("observer run-once: %w", err)
			}
			emitted, skipped, errs := writeInstinctsFromResult(layout.InstinctsDir(ident.ID), ident, res.Parsed, time.Now().UTC())
			fmt.Fprintf(stdout, "instincts emitted=%d skipped=%d soft-errors=%d duration=%s prompt_version=%s\n",
				emitted, skipped, len(errs), res.Duration.Truncate(time.Millisecond), res.PromptVersion)
			snap := res.Snapshot
			fmt.Fprintf(stdout, "limiter: session=%d/hour=%d/failures=%d circuit_open=%t\n",
				snap.SessionN, snap.HourN, snap.Failures, snap.CircuitOpen)
			for _, e := range errs {
				fmt.Fprintf(cmd.ErrOrStderr(), "  soft: %s\n", e)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().IntVar(&windowSize, "window", observerDefaultWindowSize, "max recent observations to send to Claude")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "render the prompt and exit without spawning claude --print")
	cmd.Flags().StringVar(&out, "out", "", "with --dry-run, write the rendered prompt to this path instead of stdout")
	cmd.Flags().StringVar(&model, "model", "", "override the claude model (default: haiku)")
	cmd.Flags().IntVar(&maxCalls, "max-calls", 0, "override the per-session LLM call cap (default: 10)")
	return cmd
}

type observerData struct {
	ProjectID    string
	ProjectName  string
	SessionID    string
	WindowSize   int
	WindowStart  string
	WindowEnd    string
	Observations string
	ExistingIDs  string
}

func buildObserverData(ident homunculus.ProjectIdentity, observations []observe.Observation, existing []*homunculus.Instinct, tpl prompts.Template) observerData {
	_ = tpl
	var b strings.Builder
	sessionID := ""
	var first, last time.Time
	for _, o := range observations {
		if sessionID == "" && o.SessionID != "" {
			sessionID = o.SessionID
		}
		if first.IsZero() || o.TS.Before(first) {
			first = o.TS
		}
		if last.IsZero() || o.TS.After(last) {
			last = o.TS
		}
		b.Write(renderObservationLine(o))
		b.WriteByte('\n')
	}
	// build a short, deduplicated existing-id preview so the LLM
	// avoids re-minting near-duplicates without us flooding the
	// prompt with every id ever recorded.
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(existing))
	for _, in := range existing {
		if _, dup := seen[in.ID]; dup {
			continue
		}
		seen[in.ID] = struct{}{}
		ids = append(ids, in.ID)
	}
	sort.Strings(ids)
	preview := ids
	if len(preview) > observerDefaultExistingPreview {
		preview = preview[len(preview)-observerDefaultExistingPreview:]
	}
	existingBlock := strings.Join(preview, ", ")
	if existingBlock == "" {
		existingBlock = "(none yet — this is the first observer pass for this project)"
	}
	return observerData{
		ProjectID:    ident.ID,
		ProjectName:  ident.Name,
		SessionID:    sessionID,
		WindowSize:   len(observations),
		WindowStart:  formatTS(first),
		WindowEnd:    formatTS(last),
		Observations: b.String(),
		ExistingIDs:  existingBlock,
	}
}

// maxRenderedFieldBytes bounds each free-form field (tool_input,
// tool_output, prompt) in the observation slice sent to Claude. The
// fix that made the live-hook corpus readable (= mapping the nested
// payload's tool_name / tool_input / tool_response across to the flat
// Observation) also made every record carry its full tool_input and
// tool_response; a 200-record window of raw shell output ballooned the
// rendered prompt ~25x (16KB → 400KB). Capping each free-form field
// keeps a pattern-identifying preview while holding the prompt to a
// few tens of KB so a fuller window cannot blow Haiku's context and
// silently regress the loop back to zero instincts.
const maxRenderedFieldBytes = 512

// renderObservationLine marshals one observation for the prompt slice
// with its free-form fields capped. Identity fields (ts / event /
// tool / cwd / session_id) are kept whole because they are small and
// are the primary mining signal; tool_input / tool_output / prompt are
// previewed so a single huge tool_response cannot dominate the prompt.
func renderObservationLine(o observe.Observation) []byte {
	m := map[string]any{
		"ts":    o.TS.UTC().Format(time.RFC3339Nano),
		"event": o.Event,
	}
	if o.SessionID != "" {
		m["session_id"] = o.SessionID
	}
	if o.Tool != "" {
		m["tool"] = o.Tool
	}
	if o.CWD != "" {
		m["cwd"] = o.CWD
	}
	if len(o.ToolInput) > 0 {
		m["tool_input"] = previewRawJSON(o.ToolInput)
	}
	if len(o.ToolOutput) > 0 {
		m["tool_output"] = previewRawJSON(o.ToolOutput)
	}
	if o.Prompt != "" {
		m["prompt"] = truncatePreview(o.Prompt)
	}
	line, _ := json.Marshal(m)
	return line
}

// previewRawJSON keeps a small raw JSON value verbatim (so it stays
// valid JSON in the slice) but replaces an oversized one with a
// truncated string preview so a single huge tool_response cannot
// dominate the prompt.
func previewRawJSON(r json.RawMessage) any {
	if len(r) <= maxRenderedFieldBytes {
		return r
	}
	return truncatePreview(string(r))
}

// truncatePreview caps an oversized field at maxRenderedFieldBytes,
// cutting on a RUNE boundary (not a byte boundary) so a multi-byte field
// — Japanese is 3 bytes/char and the primary language here — is never
// sliced mid-rune into a U+FFFD mojibake char. Matches the rune-safe
// truncation in scrub.go.
func truncatePreview(s string) string {
	runes := []rune(s)
	if len(runes) <= maxRenderedFieldBytes {
		return s
	}
	return string(runes[:maxRenderedFieldBytes]) + "…(truncated)"
}

func formatTS(t time.Time) string {
	if t.IsZero() {
		return "(unknown)"
	}
	return t.UTC().Format(time.RFC3339)
}

func renderForPreview(body string, data any) (string, error) {
	tpl, err := template.New("preview").Parse(body)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	if err := tpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// writeInstinctsFromResult unpacks the parsed JSON document, runs
// the host-side safety checks, and persists each entry as a
// homunculus instinct file. Returns (emitted, skipped, soft-errors).
// Skipped entries fail validation or hit a known duplicate; the
// emitted count drives the CLI summary.
func writeInstinctsFromResult(dir string, ident homunculus.ProjectIdentity, parsed map[string]any, now time.Time) (int, int, []error) {
	emitted := 0
	skipped := 0
	errs := []error{}
	raw, ok := parsed["instincts"].([]any)
	if !ok {
		return 0, 0, []error{fmt.Errorf("response missing 'instincts' array (got %T)", parsed["instincts"])}
	}
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			skipped++
			errs = append(errs, fmt.Errorf("entry was not an object: %T", item))
			continue
		}
		in, err := mapToInstinct(entry, ident, now)
		if err != nil {
			skipped++
			errs = append(errs, err)
			continue
		}
		if rule, err := checkInstinctSafety(in); err != nil {
			skipped++
			errs = append(errs, fmt.Errorf("instinct %q failed safety check (%s): %w", in.ID, rule, err))
			continue
		}
		if _, werr := homunculus.WriteInstinctFile(dir, in); werr != nil {
			skipped++
			errs = append(errs, werr)
			continue
		}
		emitted++
	}
	return emitted, skipped, errs
}

func mapToInstinct(m map[string]any, ident homunculus.ProjectIdentity, now time.Time) (*homunculus.Instinct, error) {
	id, _ := m["id"].(string)
	if id == "" {
		return nil, fmt.Errorf("entry missing id")
	}
	trigger, _ := m["trigger"].(string)
	confidence := readConfidence(m["confidence"])
	domain, _ := m["domain"].(string)
	scope, _ := m["scope"].(string)
	action, _ := m["action"].(string)
	evidenceRaw, _ := m["evidence"].([]any)
	body := buildInstinctBody(action, evidenceRaw)
	raw := map[string]any{
		"id":           id,
		"trigger":      trigger,
		"confidence":   confidence,
		"domain":       domain,
		"scope":        scope,
		"observed":     1,
		"first_seen":   now.Format(time.RFC3339),
		"last_seen":    now.Format(time.RFC3339),
		"source":       "session-observation",
		"project_id":   ident.ID,
		"project_name": ident.Name,
	}
	return &homunculus.Instinct{
		ID:         id,
		Trigger:    trigger,
		Confidence: confidence,
		Domain:     domain,
		Scope:      scope,
		Observed:   1,
		FirstSeen:  now,
		LastSeen:   now,
		Body:       body,
		Raw:        raw,
	}, nil
}

func readConfidence(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		var f float64
		_, _ = fmt.Sscanf(n, "%f", &f)
		return f
	}
	return 0
}

func buildInstinctBody(action string, evidence []any) string {
	var b strings.Builder
	b.WriteString("## Action\n")
	b.WriteString(strings.TrimSpace(action))
	b.WriteString("\n\n")
	if len(evidence) > 0 {
		b.WriteString("## Evidence\n")
		for _, e := range evidence {
			s, ok := e.(string)
			if !ok {
				continue
			}
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(s))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// checkInstinctSafety enforces the v0.9 design freeze guardrails on
// every instinct before it lands on disk. Returns a non-nil error
// when the entry violates a rule; the (rule string) names which
// rule rejected the entry so the operator sees what to fix.
func checkInstinctSafety(in *homunculus.Instinct) (string, error) {
	if len(in.Body) > 4096 {
		return "max-action-length", fmt.Errorf("body length %d exceeds 4096", len(in.Body))
	}
	if strings.Contains(in.Body, "```") {
		return "no-code-snippets", fmt.Errorf("body contains a fenced code block; instincts are prose only")
	}
	lower := strings.ToLower(in.Body)
	for _, marker := range []string{"api key", "password", "secret", "token=", "bearer "} {
		if strings.Contains(lower, marker) {
			return "no-secrets", fmt.Errorf("body looks like it includes a secret marker (%q)", marker)
		}
	}
	return "", nil
}
