package evolve

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

func TestTokenize_DropsStopwordsAndShorts(t *testing.T) {
	got := Tokenize("Always Run the test BEFORE editing a File")
	// "always", "run", "the", "before", "a", "file" are stopwords;
	// "test", "editing" survive.
	if _, ok := got["test"]; !ok {
		t.Errorf("expected 'test' token, got %v", keys(got))
	}
	if _, ok := got["editing"]; !ok {
		t.Errorf("expected 'editing' token, got %v", keys(got))
	}
	for _, stop := range []string{"always", "run", "the", "before", "a", "file"} {
		if _, ok := got[stop]; ok {
			t.Errorf("stopword %q survived tokenisation", stop)
		}
	}
}

func TestJaccard(t *testing.T) {
	a := Tokenize("data layer wrapper interface")
	b := Tokenize("data layer wrapper pattern")
	got := Jaccard(a, b)
	// shared: data, layer, wrapper (3). union: data, layer, wrapper,
	// interface, pattern (5). → 0.6
	if got < 0.59 || got > 0.61 {
		t.Errorf("Jaccard = %f, want ~0.6", got)
	}
	if Jaccard(map[string]struct{}{}, map[string]struct{}{}) != 0 {
		t.Errorf("empty-empty Jaccard should be 0")
	}
}

func TestDiscover_GroupsSimilar(t *testing.T) {
	instincts := []*homunculus.Instinct{
		mkI("io-data-layer-a", "io lives in the data layer wrapper interface"),
		mkI("io-data-layer-b", "io lives in data layer wrapper via interface"),
		mkI("io-data-layer-c", "data layer wraps io interface pattern"),
		mkI("unrelated-git", "commit message rationale architectural decision"),
	}
	clusters := Discover(instincts, nil, DefaultThresholds())
	// The 3 io instincts should form one cluster; the git one is a
	// singleton.
	var biggest int
	for _, c := range clusters {
		if len(c.Members) > biggest {
			biggest = len(c.Members)
		}
	}
	if biggest < 3 {
		t.Errorf("expected a 3-member io cluster, biggest was %d (clusters=%d)", biggest, len(clusters))
	}
}

func TestEvaluateGates_RejectsSingleton(t *testing.T) {
	c := Cluster{Members: []*homunculus.Instinct{mkI("solo", "single instinct")}}
	c.memberTokens = []map[string]struct{}{Tokenize("single instinct")}
	c.candidateTokens = c.memberTokens[0]
	v := EvaluateGates(c, DefaultThresholds())
	if v.Passed || v.RejectedAt != "gate1" {
		t.Errorf("singleton should reject at gate1, got %+v", v)
	}
}

func TestEvaluateGates_PassesCohesiveNovelCluster(t *testing.T) {
	instincts := []*homunculus.Instinct{
		mkI("io-a", "io lives in the data layer wrapper interface always"),
		mkI("io-b", "io lives in data layer wrapper interface pattern"),
		mkI("io-c", "data layer wraps io interface wrapper consistently"),
	}
	clusters := Discover(instincts, nil, DefaultThresholds())
	if len(clusters) == 0 {
		t.Fatalf("no clusters discovered")
	}
	// the biggest cluster should pass all gates (no priors → gate3/4
	// trivially pass)
	v := EvaluateGates(clusters[0], DefaultThresholds())
	if !v.Passed {
		t.Errorf("cohesive 3-member cluster with no priors should pass, got %+v", v)
	}
}

func TestEvaluateGatesWithPriorUnion_Gate3Subdivision(t *testing.T) {
	instincts := []*homunculus.Instinct{
		mkI("auth-a", "authorization guard policy capability assertion"),
		mkI("auth-b", "authorization policy guard capability token"),
	}
	// Build a cluster manually.
	clusters := Discover(instincts, nil, Thresholds{
		MemberMin: 2, CohesionMin: 0.10, LexiconCoverageMax: 0.99, RelIsolationMin: 0.0,
	})
	if len(clusters) == 0 {
		t.Fatalf("no cluster")
	}
	// Prior union containing all the cluster's vocabulary → coverage
	// is high → GATE 3' rejects.
	priorUnion := clusters[0].candidateTokens
	v := EvaluateGatesWithPriorUnion(clusters[0], priorUnion, DefaultThresholds())
	if v.RejectedAt != "gate3" {
		t.Errorf("cluster fully inside prior vocabulary should reject at gate3, got %+v", v)
	}
}

func TestClusterLabels_RoundTripAndSacredString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-labels.json")
	now := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)

	cl, err := LoadLabels(path)
	if err != nil {
		t.Fatalf("LoadLabels (missing): %v", err)
	}
	if len(cl.Labels) != 0 {
		t.Errorf("missing file should load empty catalog")
	}
	if !cl.Add("io-lives-in-data-layer", "Apply when wrapping I/O") {
		t.Errorf("Add of new label should return true")
	}
	if cl.Add("io-lives-in-data-layer", "DIFFERENT description") {
		t.Errorf("Add of existing label should return false (sacred string)")
	}
	if cl.Labels["io-lives-in-data-layer"] != "Apply when wrapping I/O" {
		t.Errorf("existing description was overwritten: %q", cl.Labels["io-lives-in-data-layer"])
	}
	if err := cl.Save(path, now); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := LoadLabels(path)
	if err != nil {
		t.Fatalf("LoadLabels (reload): %v", err)
	}
	if reloaded.Labels["io-lives-in-data-layer"] != "Apply when wrapping I/O" {
		t.Errorf("round-trip lost label")
	}
}

func TestClusterLabels_SaveBacksUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-labels.json")
	t0 := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 6, 25, 11, 0, 0, 0, time.UTC)

	cl, _ := LoadLabels(path)
	cl.Add("first", "Apply when first")
	if err := cl.Save(path, t0); err != nil {
		t.Fatalf("Save 1: %v", err)
	}
	cl.Add("second", "Apply when second")
	if err := cl.Save(path, t1); err != nil {
		t.Fatalf("Save 2: %v", err)
	}
	// A backup of the first save should exist.
	matches, _ := filepath.Glob(path + ".bak-*")
	if len(matches) == 0 {
		t.Errorf("expected a cluster-labels.json.bak-* backup after the second save")
	}
}

// Helpers

func mkI(id, body string) *homunculus.Instinct {
	return &homunculus.Instinct{
		ID:         id,
		Trigger:    "when " + id,
		Confidence: 0.7,
		Domain:     "workflow",
		Scope:      "project",
		Body:       "## Action\n" + body,
		LastSeen:   time.Now(),
	}
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
