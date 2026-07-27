package evolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

func pathCluster() Cluster {
	return Cluster{Members: []*homunculus.Instinct{
		{ID: "reindex-after-schema", Trigger: "after a schema change",
			Body: "## Action\nRun the reindex job; the mapping lives in internal/search/mapping.go."},
		{ID: "search-wrapper", Trigger: "when adding a query",
			Body: "## Action\nGo through the wrapper in internal/search rather than the client."},
	}}
}

// TestRenderRuleScopesToNamedPaths pins what a path-scoped rule is FOR:
// it fires when a file is read, which is the one moment that reaches a
// session which never named the subsystem out loud. So its globs come
// from the paths the cluster actually names.
func TestRenderRuleScopesToNamedPaths(t *testing.T) {
	art, ok := RenderRule("search-conventions", "How to change search", pathCluster(), time.Now())
	if !ok {
		t.Fatal("a cluster naming repository paths must produce a rule")
	}
	if len(art.Paths) != 1 || art.Paths[0] != "internal/search/**" {
		t.Errorf("Paths = %v, want [internal/search/**]", art.Paths)
	}
	for _, want := range []string{"paths:", "- internal/search/**", "# search-conventions"} {
		if !strings.Contains(art.Body, want) {
			t.Errorf("rule body missing %q:\n%s", want, art.Body)
		}
	}
}

// TestRenderRuleSkipsPathlessCluster pins the honest omission: a rule
// with no paths never fires, so emitting one would be dead weight that
// reads like coverage.
func TestRenderRuleSkipsPathlessCluster(t *testing.T) {
	pathless := Cluster{Members: []*homunculus.Instinct{
		{ID: "be-careful", Trigger: "when changing things", Body: "## Action\nThink before acting."},
	}}
	if _, ok := RenderRule("vague", "d", pathless, time.Now()); ok {
		t.Error("a cluster naming no paths must not produce a rule")
	}
}

// TestGlobForPathNormalisation pins the derivation rules, including the
// cases that must NOT become globs.
func TestGlobForPathNormalisation(t *testing.T) {
	cases := map[string]string{
		"internal/search/mapping.go": "internal/search/**",
		"internal/search":            "internal/search/**",
		"pkg/dockerutil":             "pkg/dockerutil/**",
		"./cmd/bough/main.go":        "cmd/bough/**",
		"README.md":                  "", // single segment: not a path scope
		"https://example.com/x":      "", // a URL is not a repo path
	}
	for in, want := range cases {
		if got := globForPath(in); got != want {
			t.Errorf("globForPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWriteRuleRespectsCurated pins that a hand-maintained rule survives
// the next run, same contract as skills.
func TestWriteRuleRespectsCurated(t *testing.T) {
	dir := t.TempDir()
	art, _ := RenderRule("search-conventions", "d", pathCluster(), time.Now())
	if _, err := WriteRule(dir, art); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "search-conventions.md")
	hand := "---\nname: search-conventions\ncurated: true\n---\n\nmine\n"
	if err := os.WriteFile(path, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteRule(dir, art); err == nil {
		t.Error("a curated rule must not be overwritten")
	}
	got, _ := os.ReadFile(path)
	if string(got) != hand {
		t.Errorf("curated rule was modified:\n%s", got)
	}
}

// TestSkillCoverageRoundTrip pins the registry: what a skill delivers is
// recorded per-slug so removing one skill removes exactly its coverage.
func TestSkillCoverageRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skill-coverage.json")
	cov, err := LoadSkillCoverage(path)
	if err != nil {
		t.Fatal(err)
	}
	cov.Record("search-conventions", []string{"reindex-after-schema", "search-wrapper"})
	cov.Record("io-data-layer", []string{"wrap-io"})
	if err := cov.Save(path, time.Now()); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadSkillCoverage(path)
	if err != nil {
		t.Fatal(err)
	}
	covered := reloaded.CoveredIDs()
	for _, id := range []string{"reindex-after-schema", "search-wrapper", "wrap-io"} {
		if _, ok := covered[id]; !ok {
			t.Errorf("covered set missing %q", id)
		}
	}
	if len(covered) != 3 {
		t.Errorf("covered = %v, want exactly 3 ids", covered)
	}
}

// TestSkillCoverageReRecordReplaces pins that a re-evolved skill covers
// what it covers NOW. Carrying forward ids it dropped would keep
// suppressing instincts nothing delivers any more — the worst outcome,
// since the knowledge would be in neither path.
func TestSkillCoverageReRecordReplaces(t *testing.T) {
	cov := &SkillCoverage{BySkill: map[string][]string{}}
	cov.Record("skill", []string{"a", "b", "c"})
	cov.Record("skill", []string{"a"})
	covered := cov.CoveredIDs()
	if len(covered) != 1 {
		t.Errorf("covered = %v, want only the ids the skill currently delivers", covered)
	}
	if _, ok := covered["a"]; !ok {
		t.Error("the still-delivered id must remain covered")
	}
}

// TestSkillCoverageAbsentIsEmpty pins that a corpus which has never
// evolved covers nothing, rather than erroring.
func TestSkillCoverageAbsentIsEmpty(t *testing.T) {
	cov, err := LoadSkillCoverage(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("absent registry must not error: %v", err)
	}
	if len(cov.CoveredIDs()) != 0 {
		t.Error("absent registry must cover nothing")
	}
}
