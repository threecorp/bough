package evolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// deploySkills creates n skill directories with a SKILL.md, mimicking a
// deployed portfolio.
func deploySkills(t *testing.T, dir string, slugs ...string) {
	t.Helper()
	for _, slug := range slugs {
		d := filepath.Join(dir, slug)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+slug+"\n---\n\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func coverageFile(t *testing.T, dir string, entries map[string][]string) string {
	t.Helper()
	path := filepath.Join(dir, "skill-coverage.json")
	cov := &SkillCoverage{BySkill: map[string][]string{}}
	for slug, ids := range entries {
		cov.Record(slug, ids)
	}
	if err := cov.Save(path, time.Now()); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestExclusionBlockedWithoutDeployedSkills is the invariant the whole
// gate exists for: flipping the switch early removes the knowledge from
// BOTH paths — the skill does not exist to be pulled, and the instincts
// are no longer pushed.
func TestExclusionBlockedWithoutDeployedSkills(t *testing.T) {
	skillsDir := t.TempDir()
	deployed := t.TempDir() // nothing deployed
	cov := coverageFile(t, t.TempDir(), map[string][]string{"a": {"i1"}})

	r, _ := ExclusionReadiness(skillsDir, deployed, cov)
	if r.Ready() {
		t.Fatal("exclusion must NOT be ready with no deployed skills")
	}
	blockers := r.Blockers()
	if len(blockers) == 0 {
		t.Fatal("a refusal must name what is missing")
	}
	joined := strings.Join(detailsOf(blockers), " ")
	if !strings.Contains(joined, "need") {
		t.Errorf("the blocker should say what is required, got: %s", joined)
	}
}

// TestExclusionBlockedWithEmptyCoverage pins the other blocking check:
// an empty registry cannot be acted on — it either suppresses nothing or
// everything depending on how the reader interprets it.
func TestExclusionBlockedWithEmptyCoverage(t *testing.T) {
	skillsDir := t.TempDir()
	deploySkills(t, skillsDir, "a", "b", "c")
	cov := coverageFile(t, t.TempDir(), nil)

	if r, _ := ExclusionReadiness(skillsDir, skillsDir, cov); r.Ready() {
		t.Error("an empty coverage registry must block the switch")
	}
}

// TestExclusionReadyWhenBothHold pins the passing case, so the gate is
// not simply "always WAIT" — a gate that can never open is the same as
// not having the feature.
func TestExclusionReadyWhenBothHold(t *testing.T) {
	skillsDir := t.TempDir()
	deploySkills(t, skillsDir, "a", "b", "c")
	cov := coverageFile(t, t.TempDir(), map[string][]string{
		"a": {"i1"}, "b": {"i2"}, "c": {"i3"},
	})

	r, _ := ExclusionReadiness(skillsDir, skillsDir, cov)
	if !r.Ready() {
		t.Errorf("expected ready, blockers: %v", detailsOf(r.Blockers()))
	}
}

// TestStaleCoverageIsAdvisoryNotBlocking pins the distinction between
// "this must hold" and "you should know". A registry entry whose skill
// is gone is worth surfacing — those ids would stay suppressed while
// nothing delivers them — but it does not by itself make exclusion
// unsafe for the skills that DO exist.
func TestStaleCoverageIsAdvisoryNotBlocking(t *testing.T) {
	skillsDir := t.TempDir()
	deploySkills(t, skillsDir, "a", "b", "c")
	cov := coverageFile(t, t.TempDir(), map[string][]string{
		"a": {"i1"}, "b": {"i2"}, "c": {"i3"}, "deleted-skill": {"i9"},
	})

	r, _ := ExclusionReadiness(skillsDir, skillsDir, cov)
	if !r.Ready() {
		t.Errorf("stale coverage must not block: %v", detailsOf(r.Blockers()))
	}
	var found bool
	for _, c := range r.Checks {
		if !c.Blocking && !c.Passed && strings.Contains(c.Detail, "deleted-skill") {
			found = true
		}
	}
	if !found {
		t.Error("stale coverage must still be reported as advisory")
	}
}

// TestReadinessNamesTheMissingThing pins the reporting contract: a gate
// that says "not ready (0.62)" tells the operator nothing about what to
// go do, so every blocker carries a concrete detail.
func TestReadinessNamesTheMissingThing(t *testing.T) {
	r, _ := ExclusionReadiness(t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "missing.json"))
	for _, b := range r.Blockers() {
		if strings.TrimSpace(b.Detail) == "" {
			t.Errorf("blocker %q has no detail", b.Name)
		}
		if strings.TrimSpace(b.Name) == "" {
			t.Error("a blocker with no name cannot be acted on")
		}
	}
}

func detailsOf(cs []ReadinessCheck) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Detail)
	}
	return out
}
