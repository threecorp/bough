package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/evolve"
	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// exclusionFixture builds a monorepo whose .bough.yaml REQUESTS the
// exclusion, with a coverage registry recording one covered instinct.
// Whether skills are deployed is the variable under test.
func exclusionFixture(t *testing.T, deploySkills bool) (monoRoot, projectID string, layout homunculus.Layout) {
	t.Helper()
	monoRoot = t.TempDir()
	yaml := "schema_version: 2\nmonorepo_root: .\n" +
		"repositories:\n  - name: app\n" +
		"registry:\n  path: .bough-ports.json\n" +
		"instinct:\n  enabled: true\n  exclude_skill_covered: true\n"
	if err := os.WriteFile(filepath.Join(monoRoot, ".bough.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	corpus := t.TempDir()
	t.Setenv(homunculus.DefaultDirEnv, corpus)
	layout = homunculus.NewLayout()
	projectID = "proj-exclusion"
	if err := layout.EnsureProjectDirs(projectID); err != nil {
		t.Fatal(err)
	}

	cov := &evolve.SkillCoverage{BySkill: map[string][]string{}}
	cov.Record("search-conventions", []string{"reindex-after-schema"})
	if err := cov.Save(layout.SkillCoverageFile(projectID), time.Now()); err != nil {
		t.Fatal(err)
	}

	if deploySkills {
		for _, slug := range []string{"a", "b", "search-conventions"} {
			d := filepath.Join(monoRoot, ".claude", "skills", slug)
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+slug+"\n---\n\nbody\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// The registry's slug must also exist in the evolved dir, or the
		// advisory stale check fires (it should not block, but the fixture
		// should reflect a healthy portfolio).
		d := filepath.Join(layout.EvolvedSkillsDir(projectID), "search-conventions")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: search-conventions\n---\n\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return monoRoot, projectID, layout
}

// TestExclusionHeldByGateDespiteConfig is the invariant that makes the
// switch safe to expose at all: an operator can ASK for it, and until
// the portfolio is actually deployed the gate withholds it. Honouring
// the flag alone would remove that knowledge from both delivery paths at
// once, and the symptom — the loop goes quiet — does not point at the
// cause.
func TestExclusionHeldByGateDespiteConfig(t *testing.T) {
	monoRoot, projectID, layout := exclusionFixture(t, false) // nothing deployed

	got := skillCoveredExclusions(monoRoot, projectID, layout)
	if len(got) != 0 {
		t.Errorf("exclusion applied while the gate says WAIT: %v", got)
	}
}

// TestExclusionAppliesOnceGateOpens pins that the gate is not simply
// "always WAIT" — a gate that can never open is the same as not having
// the feature.
func TestExclusionAppliesOnceGateOpens(t *testing.T) {
	monoRoot, projectID, layout := exclusionFixture(t, true)

	got := skillCoveredExclusions(monoRoot, projectID, layout)
	if _, ok := got["reindex-after-schema"]; !ok {
		t.Errorf("exclusion did not apply with a deployed portfolio: %v", got)
	}
}

// TestExclusionNotAppliedWithoutRequest pins that a ready gate does not
// silently start suppressing: the operator still has to ask.
func TestExclusionNotAppliedWithoutRequest(t *testing.T) {
	monoRoot, projectID, layout := exclusionFixture(t, true)
	// Rewrite the config without the request.
	yaml := "schema_version: 2\nmonorepo_root: .\n" +
		"repositories:\n  - name: app\n" +
		"registry:\n  path: .bough-ports.json\n" +
		"instinct:\n  enabled: true\n"
	if err := os.WriteFile(filepath.Join(monoRoot, ".bough.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := skillCoveredExclusions(monoRoot, projectID, layout); len(got) != 0 {
		t.Errorf("a ready gate must not suppress without the request: %v", got)
	}
}

// TestExclusionReadinessLineNamesBlockers pins the reporting contract at
// the doctor boundary: WAIT must come with the specific missing
// precondition, not a bare refusal.
func TestExclusionReadinessLineNamesBlockers(t *testing.T) {
	_, lines := exclusionReadinessLines(newGateEnv())
	if len(lines) == 0 {
		t.Fatal("the readiness line must say something")
	}
	joined := strings.Join(lines, " ")
	// Whatever the state of the machine running the test, the line must be
	// one of the three self-describing verdicts.
	for _, marker := range []string{"WAIT", "READY", "ON —"} {
		if strings.Contains(joined, marker) {
			return
		}
	}
	t.Errorf("readiness line is not self-describing: %v", lines)
}
