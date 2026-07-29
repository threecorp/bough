package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ikeikeikeike/bough/internal/evolve"
)

// deployFixture writes a minimal evolved skill dir.
func deployFixture(t *testing.T, evolvedDir, slug string) {
	t.Helper()
	d := filepath.Join(evolvedDir, slug)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+slug+"\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func emptyRegistry() *evolve.RetireRegistry {
	return &evolve.RetireRegistry{Slugs: map[string]evolve.RetiredSkill{}}
}

// TestRetiredSlugIsNotLinkedAndItsStaleLinkIsPruned is the port of the
// reference system's deploy-time guard: retirement is recorded in a
// registry, but a recorded rejection the host can still LOAD is a
// rejection that never happened. The write-time guard alone left
// exactly that hole — the dir written before the retirement kept being
// re-linked on every pass.
func TestRetiredSlugIsNotLinkedAndItsStaleLinkIsPruned(t *testing.T) {
	root := t.TempDir()
	evolved := filepath.Join(root, "evolved", "skills")
	deployFixture(t, evolved, "alive")
	deployFixture(t, evolved, "rejected")

	// An earlier pass linked both.
	var out, errOut bytes.Buffer
	if linked := deployProjectSkills(&out, &errOut, evolved, root, emptyRegistry()); !linked["alive"] || !linked["rejected"] {
		t.Fatalf("precondition: both should link first, got %v", linked)
	}

	reg := emptyRegistry()
	reg.Slugs["rejected"] = evolve.RetiredSkill{Reason: "operator rejected it"}
	out.Reset()
	linked := deployProjectSkills(&out, &errOut, evolved, root, reg)

	if linked["rejected"] {
		t.Error("a retired slug must not be in the linked set")
	}
	if !linked["alive"] {
		t.Error("the healthy slug must still link")
	}
	dest := filepath.Join(root, ".claude", "skills", "rejected")
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Errorf("the stale symlink for the retired slug must be pruned, lstat err=%v", err)
	}
	// Reversible by construction: the evolved dir itself is untouched.
	if _, err := os.Stat(filepath.Join(evolved, "rejected", "SKILL.md")); err != nil {
		t.Errorf("the evolved dir must never be deleted: %v", err)
	}
}

// TestMergedSlugCountsAsRetiredAtDeploy: a slug folded into another
// skill must stop shipping under its old label, or the consolidation
// silently undoes itself.
func TestMergedSlugCountsAsRetiredAtDeploy(t *testing.T) {
	root := t.TempDir()
	evolved := filepath.Join(root, "evolved", "skills")
	deployFixture(t, evolved, "old-label")
	deployFixture(t, evolved, "new-label")

	reg := emptyRegistry()
	reg.MergedInto = map[string]string{"old-label": "new-label"}
	var out, errOut bytes.Buffer
	linked := deployProjectSkills(&out, &errOut, evolved, root, reg)
	if linked["old-label"] {
		t.Error("a merged-away slug must not link")
	}
	if !linked["new-label"] {
		t.Error("the merge target must link")
	}
}

// TestPruneNeverTouchesForeignEntries: only symlinks that point into
// the evolved tree are deploy's to manage. A hand-authored skill dir
// and a symlink to somewhere else must survive every pass.
func TestPruneNeverTouchesForeignEntries(t *testing.T) {
	root := t.TempDir()
	evolved := filepath.Join(root, "evolved", "skills")
	deployFixture(t, evolved, "mine")
	projectDir := filepath.Join(root, ".claude", "skills")
	if err := os.MkdirAll(filepath.Join(projectDir, "hand-authored"), 0o755); err != nil {
		t.Fatal(err)
	}
	foreignTarget := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(foreignTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreignTarget, filepath.Join(projectDir, "foreign-link")); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	deployProjectSkills(&out, &errOut, evolved, root, emptyRegistry())

	if _, err := os.Stat(filepath.Join(projectDir, "hand-authored")); err != nil {
		t.Errorf("hand-authored dir must survive: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(projectDir, "foreign-link")); err != nil {
		t.Errorf("foreign symlink must survive: %v", err)
	}
}

// TestNilRegistryLeavesLinksUntouched: an unreadable registry answers
// neither "may I link?" nor "may I prune?", so the only safe deploy is
// no deploy — and the caller must be able to tell that apart from
// "linked nothing" (nil vs empty), or it would wipe coverage.
func TestNilRegistryLeavesLinksUntouched(t *testing.T) {
	root := t.TempDir()
	evolved := filepath.Join(root, "evolved", "skills")
	deployFixture(t, evolved, "existing")
	var out, errOut bytes.Buffer
	if linked := deployProjectSkills(&out, &errOut, evolved, root, emptyRegistry()); !linked["existing"] {
		t.Fatal("precondition link failed")
	}

	linked := deployProjectSkills(&out, &errOut, evolved, root, nil)
	if linked != nil {
		t.Errorf("nil registry must return nil (indeterminate), got %v", linked)
	}
	if _, err := os.Lstat(filepath.Join(root, ".claude", "skills", "existing")); err != nil {
		t.Errorf("existing links must be left exactly as they were: %v", err)
	}
}
