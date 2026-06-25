package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/ikeikeikeike/bough/internal/observe"
	"github.com/ikeikeikeike/bough/internal/prompts"
)

// Defaults pin the canonical Claude CLI invocation. Observer-loop
// and v0.9.1 evolve override these via .bough.yaml; v0.9.0 ships
// the pinned values so a fresh `bough observer run-once` produces
// the same calls the dogfood team analysed during the design
// freeze.
const (
	DefaultBin             = "claude"
	DefaultModel           = "haiku"
	DefaultMaxTurns        = 4
	DefaultTimeout         = 120 * time.Second
	DefaultRetryOnce       = true
	DefaultOutputFormat    = "json"
	DefaultPermissionMode  = "bypassPermissions"
	DefaultAllowedTools    = "" // bough rejects the Write tool; the host writes files itself.
)

// ErrCLINotFound is returned by Generate when the configured Bin is
// not on PATH. bough doctor surfaces it as a precondition failure.
var ErrCLINotFound = errors.New("claudecli: binary not found on PATH")

// ErrEmptyOutput is returned when the subprocess exits 0 but writes
// nothing to stdout (= rare but observed when the model emits only
// "I cannot help with that"-style refusals that the Claude CLI
// later strips). Treated as a transient failure → eligible for the
// single retry.
var ErrEmptyOutput = errors.New("claudecli: empty stdout")

// ErrSchemaViolation is returned when the JSON parses but does not
// match the expected shape. Caller treats this as terminal (= no
// retry) because the prompt produced wrong-shape output and a
// second call would do the same.
var ErrSchemaViolation = errors.New("claudecli: response did not match schema")

// Provider is the bough-side handle to the Claude CLI subprocess.
// Construct via NewProvider; tests override Bin / Now / FakeExec
// to avoid spawning a real subprocess.
type Provider struct {
	Bin             string
	Model           string
	MaxTurns        int
	Timeout         time.Duration
	OutputFormat    string
	PermissionMode  string
	AllowedTools    string
	JSONSchemaPath  string // optional --json-schema file
	Limiter         *Limiter

	// FakeExec, when non-nil, is invoked instead of spawning a
	// real subprocess. Tests use this to inject canned outputs.
	FakeExec func(ctx context.Context, args []string, env []string, stdin io.Reader, prompt string) ([]byte, error)
}

// NewProvider returns a Provider wired with the v0.9 defaults. The
// returned Limiter is shared across every Generate call on this
// Provider; sharing it across multiple Providers is the caller's
// job (= the observer + evolve callers both use one Limiter so the
// self-DoS cap covers the whole bough process).
func NewProvider() *Provider {
	return &Provider{
		Bin:            DefaultBin,
		Model:          DefaultModel,
		MaxTurns:       DefaultMaxTurns,
		Timeout:        DefaultTimeout,
		OutputFormat:   DefaultOutputFormat,
		PermissionMode: DefaultPermissionMode,
		AllowedTools:   DefaultAllowedTools,
		Limiter:        NewLimiter(),
	}
}

// GenerateRequest carries the per-call inputs Generate renders into
// the chosen prompt template before spawning Claude.
type GenerateRequest struct {
	Template prompts.Template
	Data     any
}

// GenerateResult holds the parsed structured output plus the audit
// metadata bough doctor / `.evolve/judgements/` consume.
type GenerateResult struct {
	Raw           []byte           // raw stdout bytes
	Parsed        map[string]any   // top-level JSON document
	PromptVersion string           // Template.Version pinned for cache key derivation
	Model         string           // resolved model id
	Snapshot      Snapshot         // limiter state after the call
	Duration      time.Duration    // wall-clock cost
}

// Generate sends one call to the Claude CLI. The flow is:
//
//   1. Acquire a limiter slot. Self-DoS or breaker → return error
//      without touching the subprocess.
//   2. Render the template against req.Data via text/template.
//   3. Spawn `claude -p <prompt> --model <model> --max-turns N
//      --output-format json [--json-schema path]` with a sanitised
//      env (= ANTHROPIC_API_KEY etc stripped).
//   4. Time out at p.Timeout; on transient failure (timeout / empty
//      stdout / non-zero exit with empty stderr), retry once.
//   5. Unmarshal the stdout; surface Parsed + Raw + audit metadata.
//
// FakeExec replaces step 3 for tests.
func (p *Provider) Generate(ctx context.Context, req GenerateRequest) (*GenerateResult, error) {
	if p.Limiter == nil {
		return nil, errors.New("claudecli.Generate: Limiter is nil")
	}
	if err := p.Limiter.Acquire(); err != nil {
		return nil, err
	}

	body, err := renderTemplate(req.Template.Body, req.Data)
	if err != nil {
		p.Limiter.RecordFailure()
		return nil, err
	}

	args := p.buildArgs()
	env := observe.SanitizeAnthropicEnv(os.Environ())
	start := time.Now()

	raw, err := p.invoke(ctx, args, env, body)
	dur := time.Since(start)

	if err != nil {
		if DefaultRetryOnce && isTransient(err) {
			raw, err = p.invoke(ctx, args, env, body)
			dur = time.Since(start)
		}
		if err != nil {
			p.Limiter.RecordFailure()
			return nil, err
		}
	}

	parsed, perr := parseJSON(raw)
	if perr != nil {
		p.Limiter.RecordFailure()
		return nil, perr
	}

	p.Limiter.RecordSuccess()
	return &GenerateResult{
		Raw:           raw,
		Parsed:        parsed,
		PromptVersion: req.Template.Version,
		Model:         p.Model,
		Snapshot:      p.Limiter.Snapshot(),
		Duration:      dur,
	}, nil
}

// GenerateRaw spawns the Claude CLI against an already-rendered
// prompt body, skipping the text/template pass Generate runs. The
// evolve judge uses this because its prompt has already been
// rendered against the cluster data — re-rendering would choke on
// any literal `{{` an instinct body contains (= e.g. an instinct
// about Go templates). Returns the raw stdout bytes + the limiter
// snapshot for the audit log.
func (p *Provider) GenerateRaw(ctx context.Context, promptBody string) ([]byte, Snapshot, error) {
	if p.Limiter == nil {
		return nil, Snapshot{}, errors.New("claudecli.GenerateRaw: Limiter is nil")
	}
	if err := p.Limiter.Acquire(); err != nil {
		return nil, p.Limiter.Snapshot(), err
	}
	args := p.buildArgs()
	env := observe.SanitizeAnthropicEnv(os.Environ())
	raw, err := p.invoke(ctx, args, env, promptBody)
	if err != nil {
		if DefaultRetryOnce && isTransient(err) {
			raw, err = p.invoke(ctx, args, env, promptBody)
		}
		if err != nil {
			p.Limiter.RecordFailure()
			return nil, p.Limiter.Snapshot(), err
		}
	}
	p.Limiter.RecordSuccess()
	return raw, p.Limiter.Snapshot(), nil
}

func (p *Provider) buildArgs() []string {
	args := []string{
		// Prompt is passed via -p<prompt> after sub.Run binds; we
		// only need the post-prompt flags here.
		"--model", p.Model,
		"--max-turns", strconv.Itoa(p.MaxTurns),
		"--output-format", p.OutputFormat,
		"--permission-mode", p.PermissionMode,
	}
	if p.AllowedTools != "" {
		args = append(args, "--allowedTools", p.AllowedTools)
	}
	if p.JSONSchemaPath != "" {
		args = append(args, "--json-schema", p.JSONSchemaPath)
	}
	return args
}

func (p *Provider) invoke(ctx context.Context, args []string, env []string, prompt string) ([]byte, error) {
	if p.FakeExec != nil {
		return p.FakeExec(ctx, args, env, nil, prompt)
	}
	if _, err := exec.LookPath(p.Bin); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrCLINotFound, p.Bin)
	}
	cctx := ctx
	if p.Timeout > 0 {
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, p.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(cctx, p.Bin, append([]string{"-p", prompt}, args...)...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	out := bytes.TrimSpace(stdout.Bytes())
	if runErr != nil {
		// Combine stderr for the diagnostic but treat the exit
		// failure itself as the error; transient classifier
		// keys on whether stdout was empty.
		return out, fmt.Errorf("claudecli.invoke: %w: stderr=%s", runErr, strings.TrimSpace(stderr.String()))
	}
	if len(out) == 0 {
		return nil, ErrEmptyOutput
	}
	return out, nil
}

func renderTemplate(body string, data any) (string, error) {
	tpl, err := template.New("prompt").Parse(body)
	if err != nil {
		return "", fmt.Errorf("claudecli.renderTemplate: %w", err)
	}
	var b bytes.Buffer
	if err := tpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("claudecli.renderTemplate: %w", err)
	}
	return b.String(), nil
}

func parseJSON(raw []byte) (map[string]any, error) {
	out := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: %v (raw=%q)", ErrSchemaViolation, err, raw)
	}
	return out, nil
}

func isTransient(err error) bool {
	if errors.Is(err, ErrEmptyOutput) {
		return true
	}
	if err == nil {
		return false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "signal: killed"),
		strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "rate limit"),
		strings.Contains(msg, "429"),
		strings.Contains(msg, "5"):
		return true
	}
	return false
}
