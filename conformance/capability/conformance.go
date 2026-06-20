// Package capability is the CapabilityCompiler contract test suite.
// Plugin authors and downstream consumers verify their compile
// pipeline conforms to the v0.6 dispatch contract by invoking Run
// with a fresh capability.Compiler + emitter registry.
//
// The suite exercises:
//
//   - synthesise: every (instinct × kind × target) combination
//     produces one CapabilityArtifact.
//   - Checksum: ComputeChecksum stamps every artifact deterministically.
//   - Provenance: instinct IDs round-trip into the artifact metadata.
//   - Emitter dispatch: each Target.Format hits exactly one
//     registered emitter per artifact.
//   - DryRun: skips emit but still materialises artifacts +
//     checksums.
//
// Memory-side concerns (= round-trip Export/Import of the rendered
// bytes) live in conformance/memory; this suite stays close to the
// compile pipeline so a v0.6.x SkillX adapter can run it in isolation.
package capability

import (
	"context"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/capability"
	"github.com/ikeikeikeike/bough/pkg/schema"
	capapi "github.com/ikeikeikeike/bough/plugins/capability/api"
)

// Config drives one conformance run. Compiler + Registry are
// constructed by the suite when nil so the default invocation —
// `capability.Run(t, capability.Config{})` — works without ceremony.
type Config struct {
	// Compiler under test. When nil, the suite builds a default
	// compiler over a fresh registry that holds the provided
	// Emitters (or one in-tree fake when len(Emitters) == 0).
	Compiler *capability.Compiler

	// Emitters supply the registry the default compiler should
	// dispatch through. Plugin authors override this to test their
	// own emitter against the canonical compile loop.
	Emitters []capapi.Emitter
}

// Run drives the contract. Tests pass a non-nil Compiler when they
// want to validate a custom registry; the default path constructs a
// fake emitter that records every Emit call.
func Run(t *testing.T, cfg Config) {
	t.Helper()

	registry := capability.NewRegistry()
	if len(cfg.Emitters) == 0 {
		cfg.Emitters = []capapi.Emitter{&recordingEmitter{format: "conformance"}}
	}
	for _, e := range cfg.Emitters {
		registry.Register(e)
	}
	compiler := cfg.Compiler
	if compiler == nil {
		compiler = capability.NewCompiler(registry)
	}

	t.Run("Dispatch", func(t *testing.T) { runDispatch(t, compiler, cfg) })
	t.Run("DryRun", func(t *testing.T) { runDryRun(t, compiler, cfg) })
	t.Run("Checksum", func(t *testing.T) { runChecksum(t, compiler, cfg) })
}

// runDispatch primes one instinct and asserts every (kind, target)
// combination produces one artifact + one emission.
func runDispatch(t *testing.T, compiler *capability.Compiler, cfg Config) {
	t.Helper()
	inst := schema.Instinct{
		InstinctCandidate: schema.InstinctCandidate{
			ID:           "conformance-rule-1",
			Rule:         "prefer early returns",
			HowToApply:   "when nesting depth would exceed 2",
			Confidence:   0.8,
			Source:       schema.TraceSourceExplicitFeedback,
			SourceTraces: []string{"trace-conformance-1"},
		},
		EvidenceRefs: []string{"trace-conformance-1"},
	}
	formats := registryFormats(cfg.Emitters)
	if len(formats) == 0 {
		t.Skip("no emitters provided")
	}
	targets := make([]schema.Target, 0, len(formats))
	for _, f := range formats {
		targets = append(targets, schema.Target{Format: f})
	}
	result, err := compiler.Compile(context.Background(), &capability.CompileRequest{
		SourceInstincts: []schema.Instinct{inst},
		TargetKinds:     []schema.CapabilityArtifactKind{schema.ArtifactKindRule},
		Targets:         targets,
		Scope:           schema.Scope{Level: schema.ScopeWorktree, WorktreeID: "F-conf", RepoName: "conf"},
	}, capapi.EmitOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := len(result.Artifacts); got != len(targets) {
		t.Errorf("expected %d artifacts (one per target), got %d", len(targets), got)
	}
	if got := len(result.Emissions); got != len(targets) {
		t.Errorf("expected %d emissions, got %d", len(targets), got)
	}
	for _, a := range result.Artifacts {
		if len(a.Provenance.InstinctIDs) != 1 || a.Provenance.InstinctIDs[0] != inst.ID {
			t.Errorf("provenance.InstinctIDs should round-trip the source id: %+v", a.Provenance)
		}
	}
}

// runDryRun asserts DryRun=true still materialises artifacts but
// skips emit.
func runDryRun(t *testing.T, compiler *capability.Compiler, cfg Config) {
	t.Helper()
	formats := registryFormats(cfg.Emitters)
	if len(formats) == 0 {
		t.Skip("no emitters provided")
	}
	for _, em := range cfg.Emitters {
		if rec, ok := em.(*recordingEmitter); ok {
			rec.calls = 0
		}
	}
	result, err := compiler.Compile(context.Background(), &capability.CompileRequest{
		SourceInstincts: []schema.Instinct{{InstinctCandidate: schema.InstinctCandidate{ID: "dry-1", Rule: "x"}}},
		Targets:         []schema.Target{{Format: formats[0]}},
		DryRun:          true,
	}, capapi.EmitOptions{})
	if err != nil {
		t.Fatalf("Compile DryRun: %v", err)
	}
	if len(result.Artifacts) == 0 {
		t.Error("DryRun should still produce artifacts")
	}
	if len(result.Emissions) != 0 {
		t.Errorf("DryRun should skip emissions, got %d", len(result.Emissions))
	}
	for _, em := range cfg.Emitters {
		if rec, ok := em.(*recordingEmitter); ok && rec.calls != 0 {
			t.Errorf("recordingEmitter %q should not have been called on DryRun: %d", rec.format, rec.calls)
		}
	}
}

// runChecksum asserts ComputeChecksum stamps every artifact and is
// deterministic across regenerations of the same logical artifact.
func runChecksum(t *testing.T, compiler *capability.Compiler, cfg Config) {
	t.Helper()
	formats := registryFormats(cfg.Emitters)
	if len(formats) == 0 {
		t.Skip("no emitters provided")
	}
	inst := schema.Instinct{InstinctCandidate: schema.InstinctCandidate{ID: "stable", Rule: "deterministic"}}
	first, err := compiler.Compile(context.Background(), &capability.CompileRequest{
		SourceInstincts: []schema.Instinct{inst},
		Targets:         []schema.Target{{Format: formats[0]}},
	}, capapi.EmitOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(first.Artifacts) == 0 || strings.TrimSpace(first.Artifacts[0].Checksum) == "" {
		t.Fatalf("ComputeChecksum should populate the field: %+v", first.Artifacts)
	}
	want := first.Artifacts[0].Checksum
	for _, a := range first.Artifacts {
		if a.Checksum == "" {
			t.Errorf("checksum missing on artifact: %+v", a)
		}
	}
	// Recompute the canonical bytes manually and confirm they
	// match the stamped value.
	artifact := first.Artifacts[0]
	recomputed := artifact.ComputeChecksum()
	if recomputed != want {
		t.Errorf("ComputeChecksum should be deterministic: stamped=%q recomputed=%q", want, recomputed)
	}
}

// registryFormats projects the Emitter slice into format strings.
func registryFormats(emitters []capapi.Emitter) []string {
	out := make([]string, 0, len(emitters))
	for _, e := range emitters {
		out = append(out, e.Format())
	}
	return out
}

// recordingEmitter is the default emitter the suite registers when
// the caller did not supply one. It records every Emit call so
// DryRun can assert no calls happened.
type recordingEmitter struct {
	format string
	calls  int
}

func (r *recordingEmitter) Format() string { return r.format }
func (r *recordingEmitter) Emit(_ context.Context, a schema.CapabilityArtifact, _ capapi.EmitOptions) (*capapi.EmitResult, error) {
	r.calls++
	return &capapi.EmitResult{
		Filename:    a.ID + ".out",
		ContentType: "text/plain",
		Bytes:       []byte(a.Description),
	}, nil
}
