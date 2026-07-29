package evolve

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

func skillArtifact(slug, description, body string) SkillArtifact {
	return SkillArtifact{Slug: slug, Description: description, Body: body}
}

// TestCuratedSkillSurvivesReEmit is the whole point of the curated flag.
// An operator who rewrites a generated skill by hand has made a decision;
// the next evolve run silently replacing it is a loss that leaves no
// trace — the file is still there, it is just not theirs any more.
func TestCuratedSkillSurvivesReEmit(t *testing.T) {
	skillsDir := t.TempDir()
	slug := "io-data-layer"
	dir := filepath.Join(skillsDir, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	handWritten := "---\nname: " + slug + "\ncurated: true\n---\n\n# Hand-maintained\nMy own words.\n"
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(handWritten), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WriteSkill(skillsDir, skillArtifact(slug, "regenerated", "---\nname: "+slug+"\n---\n\ngenerated\n"), nil)
	if !errors.Is(err, ErrCurated) {
		t.Fatalf("WriteSkill err = %v, want ErrCurated", err)
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != handWritten {
		t.Errorf("the hand-written skill was modified:\n%s", got)
	}
}

// TestUncuratedSkillIsRefreshed pins the other half: refreshing an
// ordinary generated skill is the normal path and must keep working, or
// evolve stops being able to improve anything.
func TestUncuratedSkillIsRefreshed(t *testing.T) {
	skillsDir := t.TempDir()
	slug := "io-data-layer"
	if _, err := WriteSkill(skillsDir, skillArtifact(slug, "v1", "---\nname: "+slug+"\n---\n\nfirst\n"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSkill(skillsDir, skillArtifact(slug, "v2", "---\nname: "+slug+"\n---\n\nsecond\n"), nil); err != nil {
		t.Fatalf("refreshing an uncurated skill must work: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(skillsDir, slug, "SKILL.md"))
	if !strings.Contains(string(got), "second") {
		t.Errorf("skill was not refreshed:\n%s", got)
	}
}

// TestRetiredSlugIsNotResurrected pins the registry. Deleting the
// directory is the obvious way to reject a bad skill, and it does not
// work: the next run recreates it. Rejection has to be RECORDED.
func TestRetiredSlugIsNotResurrected(t *testing.T) {
	skillsDir := t.TempDir()
	slug := "noisy-skill"
	art := skillArtifact(slug, "d", "---\nname: "+slug+"\n---\n\nbody\n")
	if _, err := WriteSkill(skillsDir, art, nil); err != nil {
		t.Fatal(err)
	}

	reg, err := LoadRetireRegistry(skillsDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Retire(skillsDir, slug, "too generic to be useful", nil); err != nil {
		t.Fatal(err)
	}
	// Operator deletes the directory as well.
	if err := os.RemoveAll(filepath.Join(skillsDir, slug)); err != nil {
		t.Fatal(err)
	}

	// The next pass loads the registry fresh, which is where the
	// retirement recorded above takes effect.
	nextPass, lerr := LoadRetireRegistry(skillsDir)
	if lerr != nil {
		t.Fatal(lerr)
	}
	_, err = WriteSkill(skillsDir, art, nextPass)
	if !errors.Is(err, ErrRetired) {
		t.Fatalf("WriteSkill err = %v, want ErrRetired", err)
	}
	if !strings.Contains(err.Error(), "too generic") {
		t.Errorf("the recorded reason must travel with the refusal: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(skillsDir, slug, "SKILL.md")); !os.IsNotExist(serr) {
		t.Error("a retired skill was recreated on disk")
	}
}

// TestRetireRegistryPersistsAcrossLoads pins that retirement outlives
// the process — a registry only held in memory would forget on the next
// run, which is exactly the bug.
func TestRetireRegistryPersistsAcrossLoads(t *testing.T) {
	skillsDir := t.TempDir()
	reg, err := LoadRetireRegistry(skillsDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Retire(skillsDir, "gone", "not useful", nil); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadRetireRegistry(skillsDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Retired("gone") {
		t.Error("retirement did not survive a reload")
	}
	if got := reloaded.sortedSlugs(); len(got) != 1 || got[0] != "gone" {
		t.Errorf("sortedSlugs = %v, want [gone]", got)
	}
	// A fresh directory has an empty registry and no error.
	fresh, err := LoadRetireRegistry(t.TempDir())
	if err != nil || fresh.Retired("anything") {
		t.Errorf("absent registry: err=%v retired=%v, want nil/false", err, fresh.Retired("anything"))
	}
}

// TestQualityBarRejectsAdviceShapedBody is the bar's reason for
// existing: a body of generalities matches every prompt and teaches
// nothing, and it is exactly what an LLM produces from a weak cluster.
func TestQualityBarRejectsAdviceShapedBody(t *testing.T) {
	vague := skillArtifact("vague", "Be careful when making changes",
		"# Vague\n\nAlways think about the problem before you act.\nConsider the impact of your change on other people.\n")
	issues := QualityIssues(vague)
	if len(issues) == 0 {
		t.Fatal("a body with nothing concrete in it must not pass the bar")
	}
	joined := strings.Join(issues, "; ")
	if !strings.Contains(joined, "concrete identifiers") {
		t.Errorf("issues should name the missing concreteness: %v", issues)
	}
}

// TestQualityBarAcceptsConcreteBody pins the other side: a body naming
// things a reader can go look up passes.
func TestQualityBarAcceptsConcreteBody(t *testing.T) {
	concrete := skillArtifact("io-data-layer", "Apply when wrapping I/O in the data layer",
		"# io-data-layer\n\n- Call `homunculus.WriteInstinctFile` rather than writing the path yourself.\n"+
			"- Read `internal/inject/inject.go` before changing selection.\n"+
			"- The retry lives in `pkg/dockerutil`.\n")
	if issues := QualityIssues(concrete); len(issues) != 0 {
		t.Errorf("a concrete body must pass: %v", issues)
	}
}

// TestQualityBarReportsEveryIssue pins that all failures come back at
// once. An author fixing one issue per LLM round is the expensive way to
// discover there were three.
func TestQualityBarReportsEveryIssue(t *testing.T) {
	bad := skillArtifact("bad", strings.Repeat("x", descriptionMaxBytes+1),
		"# bad\n\nBe thoughtful and considerate about everything you do.\n")
	issues := QualityIssues(bad)
	if len(issues) < 2 {
		t.Errorf("issues = %v, want both the description budget AND the identifier bar", issues)
	}
}

// TestQualityBarRejectsEmptyDescription pins the host-facing failure: a
// skill with no description cannot be matched, so it is dead weight in
// the listing budget.
func TestQualityBarRejectsEmptyDescription(t *testing.T) {
	issues := QualityIssues(skillArtifact("x", "  ", "# x\n\nCall `pkg.Thing` and `other.Thing` and `third.Thing`.\n"))
	if len(issues) == 0 || !strings.Contains(strings.Join(issues, ";"), "description") {
		t.Errorf("empty description must be an issue: %v", issues)
	}
}

// TestRenderSkill_EvolvedFromCarriesPath pins the provenance upgrade:
// the id answers "which instincts became this", but only the path lets a
// reader auditing a surprising skill go read one.
func TestRenderSkill_EvolvedFromCarriesPath(t *testing.T) {
	c := Cluster{Members: []*homunculus.Instinct{
		{ID: "member-b", Path: "/corpus/instincts/member-b.md", Body: "## Action\nDo b."},
		{ID: "member-a", Path: "/corpus/instincts/member-a.md", Body: "## Action\nDo a."},
	}}
	art := RenderSkill("io-data-layer", "Apply when wrapping I/O", c, DefaultThresholds(), time.Now())
	for _, want := range []string{
		"- {id: member-a, path: /corpus/instincts/member-a.md}",
		"- {id: member-b, path: /corpus/instincts/member-b.md}",
	} {
		if !strings.Contains(art.Body, want) {
			t.Errorf("frontmatter missing %q:\n%s", want, art.Body)
		}
	}
	// id-sorted, so the frontmatter and the source-instinct block agree.
	if strings.Index(art.Body, "member-a, path") > strings.Index(art.Body, "member-b, path") {
		t.Error("evolved_from must be id-sorted so it matches the source block")
	}
}

// TestRetireSurvivesARename is the guard the handover asks for. Slug
// equality alone does not hold: clustering names a theme from its
// members, so the next pass over a barely-changed corpus produces the
// same grouping under a different label — and a registry keyed on the
// label lets the rejected knowledge straight back in.
func TestRetireSurvivesARename(t *testing.T) {
	dir := t.TempDir()
	reg := &RetireRegistry{}
	if err := reg.Retire(dir, "search-conventions", "too vague", []string{"a", "b", "c", "d"}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadRetireRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}

	// The same grouping under a new name: 3 of the retired 4 are here.
	matched, retired := reloaded.RetiredAs("indexing-conventions", []string{"a", "b", "c", "z"})
	if !retired {
		t.Fatal("a renamed cluster carrying the retired members must stay retired")
	}
	if !strings.Contains(matched, "search-conventions") || !strings.Contains(matched, "too vague") {
		t.Errorf("the refusal must name which rejection it matched and why, got %q", matched)
	}

	// A genuinely different grouping is not blocked.
	if _, retired := reloaded.RetiredAs("unrelated", []string{"x", "y", "z", "w"}); retired {
		t.Error("an unrelated cluster must not inherit someone else's rejection")
	}
	// One shared member out of four is below the bar.
	if _, retired := reloaded.RetiredAs("mostly-new", []string{"a", "x", "y", "z"}); retired {
		t.Error("a single shared member is not the same grouping")
	}
	// The exact slug is still refused, members or not.
	if _, retired := reloaded.RetiredAs("search-conventions", nil); !retired {
		t.Error("the retired slug itself must still be refused")
	}
}

// TestLegacyRetireEntriesStillLoad: entries written before members were
// recorded must keep working, and must not be silently dropped — a
// vanished entry resurrects a skill the operator rejected.
func TestLegacyRetireEntriesStillLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".retired.json"),
		[]byte(`{"retired":{"old-slug":"rejected before members were kept"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRetireRegistry(dir)
	if err != nil {
		t.Fatalf("a legacy registry must still load: %v", err)
	}
	if _, retired := reg.RetiredAs("old-slug", nil); !retired {
		t.Error("a legacy entry must still refuse its own slug")
	}
	if reg.Slugs["old-slug"].Reason != "rejected before members were kept" {
		t.Errorf("the reason must survive, got %q", reg.Slugs["old-slug"].Reason)
	}
	// With no members recorded there is nothing to overlap against, so a
	// rename cannot be caught — and guessing would be worse than not.
	if _, retired := reg.RetiredAs("renamed", []string{"a", "b"}); retired {
		t.Error("a memberless entry must not match by overlap")
	}
}

// TestUnparseableRetireEntryIsAnErrorNotASkip: an entry the reader
// cannot make sense of must stop the pass, not disappear from it. A
// registry whose entries silently vanish resurrects everything the
// operator rejected, and does so quietly.
func TestUnparseableRetireEntryIsAnErrorNotASkip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".retired.json"),
		[]byte(`{"retired":{"weird":12345}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRetireRegistry(dir); err == nil {
		t.Fatal("an unrecognised entry shape must be an error, not a skipped entry")
	}
}

// TestWriteSkillRefusesARenamedRetiredCluster drives the guard through
// the emit path it actually protects.
func TestWriteSkillRefusesARenamedRetiredCluster(t *testing.T) {
	dir := t.TempDir()
	reg := &RetireRegistry{}
	if err := reg.Retire(dir, "old-name", "not useful", []string{"m1", "m2", "m3"}); err != nil {
		t.Fatal(err)
	}
	art := SkillArtifact{
		Slug:    "new-name",
		Body:    "---\nname: new-name\n---\n\nbody\n",
		Members: []string{"m1", "m2", "m9"},
	}
	if _, err := WriteSkill(dir, art, reg); !errors.Is(err, ErrRetired) {
		t.Fatalf("emit must refuse the renamed cluster, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new-name", "SKILL.md")); err == nil {
		t.Error("nothing should have been written")
	}
}
