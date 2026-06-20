package capability

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/pkg/schema"
	capapi "github.com/ikeikeikeike/bough/plugins/capability/api"
)

// fakeEmitter records every Emit call so a test can assert which
// target the compiler dispatched to.
type fakeEmitter struct {
	format string
	emits  []schema.CapabilityArtifact
	err    error
}

func (f *fakeEmitter) Format() string { return f.format }
func (f *fakeEmitter) Emit(_ context.Context, a schema.CapabilityArtifact, _ capapi.EmitOptions) (*capapi.EmitResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.emits = append(f.emits, a)
	return &capapi.EmitResult{Filename: a.ID + ".md", ContentType: "text/markdown", Bytes: []byte(a.Description)}, nil
}

// TestRegistry_RegisterLookup pins the basic registry contract.
func TestRegistry_RegisterLookup(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeEmitter{format: "agent-skill"})
	r.Register(&fakeEmitter{format: "claude-skill"})

	got, err := r.Lookup("agent-skill")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.Format() != "agent-skill" {
		t.Errorf("Lookup returned wrong format: %q", got.Format())
	}
	formats := r.Formats()
	if len(formats) != 2 || formats[0] != "agent-skill" || formats[1] != "claude-skill" {
		t.Errorf("Formats sort: %v", formats)
	}
	if _, err := r.Lookup("unknown"); err == nil {
		t.Errorf("Lookup unknown should error")
	}
}

// TestCompile_HappyPath asserts the dispatch loop walks every
// instinct × kind × target combination and stamps a checksum on
// each artifact.
func TestCompile_HappyPath(t *testing.T) {
	reg := NewRegistry()
	agent := &fakeEmitter{format: "agent-skill"}
	claude := &fakeEmitter{format: "claude-skill"}
	reg.Register(agent)
	reg.Register(claude)
	c := NewCompiler(reg)

	inst := schema.Instinct{
		InstinctCandidate: schema.InstinctCandidate{
			ID:         "rule-1",
			Rule:       "prefer early returns",
			HowToApply: "when nesting > 2",
			Confidence: 0.7,
		},
	}
	result, err := c.Compile(context.Background(), &CompileRequest{
		SourceInstincts: []schema.Instinct{inst},
		TargetKinds:     []schema.CapabilityArtifactKind{schema.ArtifactKindRule},
		Targets:         []schema.Target{{Format: "agent-skill"}, {Format: "claude-skill"}},
	}, capapi.EmitOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(result.Artifacts) != 2 {
		t.Errorf("expected 2 artifacts (= 1 instinct × 1 kind × 2 targets): got %d", len(result.Artifacts))
	}
	if len(result.Emissions) != 2 {
		t.Errorf("expected 2 emissions: got %d", len(result.Emissions))
	}
	for _, a := range result.Artifacts {
		if a.Checksum == "" {
			t.Errorf("artifact checksum should be populated: %+v", a)
		}
	}
	if len(agent.emits) != 1 || len(claude.emits) != 1 {
		t.Errorf("each emitter should fire once: agent=%d claude=%d", len(agent.emits), len(claude.emits))
	}
}

// TestCompile_DryRunSkipsEmit asserts DryRun=true still materialises
// artifacts but does not call any emitter.
func TestCompile_DryRunSkipsEmit(t *testing.T) {
	reg := NewRegistry()
	em := &fakeEmitter{format: "agent-skill"}
	reg.Register(em)
	c := NewCompiler(reg)

	result, err := c.Compile(context.Background(), &CompileRequest{
		SourceInstincts: []schema.Instinct{{InstinctCandidate: schema.InstinctCandidate{ID: "x", Rule: "y"}}},
		TargetKinds:     []schema.CapabilityArtifactKind{schema.ArtifactKindRule},
		Targets:         []schema.Target{{Format: "agent-skill"}},
		DryRun:          true,
	}, capapi.EmitOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(result.Artifacts) != 1 {
		t.Errorf("artifacts: %d", len(result.Artifacts))
	}
	if len(result.Emissions) != 0 {
		t.Errorf("dry run should skip emit: %d", len(result.Emissions))
	}
	if len(em.emits) != 0 {
		t.Errorf("dry run should not invoke emitter: %d", len(em.emits))
	}
}

// TestCompile_UnknownTargetErrors asserts the dispatch surfaces a
// stable error when an emitter is missing.
func TestCompile_UnknownTargetErrors(t *testing.T) {
	c := NewCompiler(NewRegistry())
	_, err := c.Compile(context.Background(), &CompileRequest{
		SourceInstincts: []schema.Instinct{{InstinctCandidate: schema.InstinctCandidate{ID: "x", Rule: "y"}}},
		Targets:         []schema.Target{{Format: "agent-skill"}},
	}, capapi.EmitOptions{})
	if err == nil {
		t.Fatal("expected error on unknown target")
	}
	if !strings.Contains(err.Error(), "agent-skill") {
		t.Errorf("error should mention the missing format: %v", err)
	}
}

// TestCompile_EmitErrorPropagates verifies the dispatcher wraps
// emit errors with target + artifact context.
func TestCompile_EmitErrorPropagates(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&fakeEmitter{format: "agent-skill", err: errors.New("boom")})
	c := NewCompiler(reg)
	_, err := c.Compile(context.Background(), &CompileRequest{
		SourceInstincts: []schema.Instinct{{InstinctCandidate: schema.InstinctCandidate{ID: "x", Rule: "y"}}},
		Targets:         []schema.Target{{Format: "agent-skill"}},
	}, capapi.EmitOptions{})
	if err == nil {
		t.Fatal("expected error from emit")
	}
	if !strings.Contains(err.Error(), "agent-skill") {
		t.Errorf("error should attribute to target: %v", err)
	}
}

// TestCanonicalBytes_StableAcrossCalls pins the deterministic
// snapshot used by Checksum. Two artifacts with the same logical
// shape must hash to the same string.
func TestCanonicalBytes_StableAcrossCalls(t *testing.T) {
	a := &schema.CapabilityArtifact{
		ID:          "rule-1",
		Kind:        schema.ArtifactKindRule,
		Name:        "early returns",
		Description: "prefer early returns",
		Confidence:  0.7,
	}
	first := a.ComputeChecksum()
	second := a.ComputeChecksum()
	if first != second {
		t.Errorf("checksum should be deterministic: %q != %q", first, second)
	}
}
