package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/evolve"
	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/telemetry"
)

// exclusionFixture builds a monorepo whose .bough.yaml REQUESTS the
// exclusion, with a coverage registry recording one covered instinct.
// Whether the PULL PATH HAS FIRED is the variable under test: deploying
// a skill is not evidence that anything loads it, so the fixture seeds
// recorded pulls rather than files on disk.
func exclusionFixture(t *testing.T, pullPathFiring bool) (monoRoot, projectID string, layout homunculus.Layout) {
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

	if pullPathFiring {
		// Three registered skills, each pulled, with history behind the
		// oldest event and at least one pull inside the recent window.
		cov := &evolve.SkillCoverage{BySkill: map[string][]string{}}
		cov.Record("search-conventions", []string{"reindex-after-schema"})
		cov.Record("a", []string{"instinct-a"})
		cov.Record("b", []string{"instinct-b"})
		if err := cov.Save(layout.SkillCoverageFile(projectID), time.Now()); err != nil {
			t.Fatal(err)
		}
		tw := telemetry.NewWriter(layout.TelemetryFile(projectID))
		now := time.Now()
		for _, e := range []struct {
			slug string
			ago  time.Duration
		}{
			{"search-conventions", 20 * 24 * time.Hour}, // history anchor
			{"search-conventions", 1 * 24 * time.Hour},
			{"a", 2 * 24 * time.Hour},
			{"b", 3 * 24 * time.Hour},
		} {
			if err := tw.Append(telemetry.Event{
				TS: now.Add(-e.ago), Kind: telemetry.KindSkillPull, Slug: e.slug,
			}); err != nil {
				t.Fatal(err)
			}
		}
		// Every pulled skill exists in BOTH places: the evolved dir the
		// gate stats, and the repo-local .claude/skills the host loads
		// from. A pull only counts while the skill it loaded is still
		// there, so a fixture that skips the evolved copy is modelling a
		// deleted portfolio, not a healthy one.
		for _, slug := range []string{"a", "b", "search-conventions"} {
			for _, d := range []string{
				filepath.Join(monoRoot, ".claude", "skills", slug),
				filepath.Join(layout.EvolvedSkillsDir(projectID), slug),
			} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+slug+"\n---\n\nbody\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return monoRoot, projectID, layout
}

// TestExclusionHeldByGateDespiteConfig is the invariant that makes the
// switch safe to expose at all: an operator can ASK for it, and until
// the pull path has demonstrably fired the gate withholds it. Honouring
// the flag alone would remove that knowledge from both delivery paths at
// once, and the symptom — the loop goes quiet — does not point at the
// cause.
func TestExclusionHeldByGateDespiteConfig(t *testing.T) {
	monoRoot, projectID, layout := exclusionFixture(t, false) // nothing has been pulled

	got := skillCoveredExclusions(monoRoot, projectID, layout)
	if len(got) != 0 {
		t.Errorf("exclusion applied while the gate says WAIT: %v", got)
	}
}

// TestExclusionAppliesOnceGateOpens pins that the gate is not simply
// "always WAIT" — a gate that can never open is the same as not having
// the feature. What opens it is recorded USAGE, not files on disk.
func TestExclusionAppliesOnceGateOpens(t *testing.T) {
	monoRoot, projectID, layout := exclusionFixture(t, true)

	got := skillCoveredExclusions(monoRoot, projectID, layout)
	if _, ok := got["reindex-after-schema"]; !ok {
		t.Errorf("exclusion did not apply though the pull path is firing: %v", got)
	}
}

// TestDeployedButUnusedPortfolioDoesNotOpenTheGate is the regression for
// the defect this gate was rebuilt around: skills on disk used to be
// enough, so a portfolio nothing had ever loaded could suppress the
// push path — removing the knowledge from both at once.
func TestDeployedButUnusedPortfolioDoesNotOpenTheGate(t *testing.T) {
	monoRoot, projectID, layout := exclusionFixture(t, false)
	// Deploy the whole portfolio anyway. No pull was ever recorded.
	for _, slug := range []string{"a", "b", "search-conventions"} {
		d := filepath.Join(monoRoot, ".claude", "skills", slug)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+slug+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := skillCoveredExclusions(monoRoot, projectID, layout); len(got) != 0 {
		t.Errorf("a deployed but never-pulled portfolio must not suppress anything: %v", got)
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

// TestAdvisoryNotesAreRendered is the regression for a diagnostic that
// was computed on every doctor run and then dropped: the Ready branch
// never looked at Checks and the WAIT branch printed only Blockers(),
// which filters to the blocking ones. The cost was paid, the comment
// claimed it was rendered, and the operator could never see it.
func TestAdvisoryNotesAreRendered(t *testing.T) {
	monoRoot, projectID, layout := exclusionFixture(t, true)
	_ = monoRoot
	// Register a skill that is not on disk: a failing NON-blocking check.
	cov, err := evolve.LoadSkillCoverage(layout.SkillCoverageFile(projectID))
	if err != nil {
		t.Fatal(err)
	}
	cov.Record("a-skill-that-was-deleted", []string{"orphan-id"})
	if err := cov.Save(layout.SkillCoverageFile(projectID), time.Now()); err != nil {
		t.Fatal(err)
	}
	r := evolve.ExclusionReadiness(
		layout.EvolvedSkillsDir(projectID),
		layout.TelemetryFile(projectID),
		layout.SkillCoverageFile(projectID),
		time.Now(), evolve.DefaultExclusionWindow(),
	).WithAdvisory()

	notes := advisoryNotes(r)
	if len(notes) == 0 {
		t.Fatal("a failing advisory check must produce a rendered note")
	}
	if !strings.Contains(strings.Join(notes, "\n"), "a-skill-that-was-deleted") {
		t.Errorf("the note must name the missing skill, got %v", notes)
	}
}
