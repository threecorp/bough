package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// verdictFixture stands up a project with one quarantined instinct and a
// commented .bough.yaml, and returns the pieces every subcommand needs.
// The comments in the config are the load-bearing part of the fixture:
// keep edits the operator's own file, and a round-trip that drops their
// comments is a corruption, not a formatting choice.
func verdictFixture(t *testing.T) (root string, layout homunculus.Layout, ident homunculus.ProjectIdentity, batch string) {
	t.Helper()
	root = t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "test"},
	} {
		if out, err := gitIn(root, args...); err != nil {
			t.Skipf("git unavailable: %v (%s)", err, out)
		}
	}
	corpus := t.TempDir()
	t.Setenv(homunculus.DefaultDirEnv, corpus)

	cfg := `# the operator's own notes live here
schema_version: 2
monorepo_root: "." # anchor comment
instinct:
  gate:
    enabled: true
    # reviewed exemptions
    allow_ids:
      - existing-entry # kept from an earlier review
`
	if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	var err error
	ident, err = homunculus.DetectIdentity(root)
	if err != nil {
		t.Skipf("identity: %v", err)
	}
	layout = homunculus.NewLayout()
	if err := layout.EnsureProjectDirs(ident.ID); err != nil {
		t.Fatal(err)
	}
	batch = filepath.Join(layout.QuarantineDir(ident.ID), "20260101-000000")
	if err := os.MkdirAll(batch, 0o755); err != nil {
		t.Fatal(err)
	}
	note := "---\nid: held-note\ntrigger: before pushing a shared branch\nconfidence: 0.9\nscope: project\n---\n\n" +
		"## Action\nnever run `git push --force`; open a change instead\n"
	if err := os.WriteFile(filepath.Join(batch, "held-note.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batch, "REPORT.md"), []byte("# Policy quarantine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, layout, ident, batch
}

func gitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// keep = allowlist with the reason + restore to staging. The reason must
// land NEXT TO the id, and every comment the operator wrote must survive
// the round-trip.
func TestVerdictKeep_AllowlistsAndRestores(t *testing.T) {
	root, layout, ident, batch := verdictFixture(t)

	err := runVerdictKeep(os.Stderr, &cobra.Command{}, root, batch, "held-note",
		"this note IS the force-push rule; it names the command it forbids")
	if err != nil {
		t.Fatalf("keep: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, ".bough.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(raw)
	for _, want := range []string{
		"held-note", "IS the force-push rule", // the entry and its reason
		"existing-entry", "kept from an earlier review", // the prior entry and ITS reason
		"the operator's own notes live here", "anchor comment", // unrelated comments
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lost %q after keep:\n%s", want, cfg)
		}
	}
	if _, err := os.Stat(filepath.Join(batch, "held-note.md")); !os.IsNotExist(err) {
		t.Error("the held file is still in quarantine after keep")
	}
	if _, err := os.Stat(filepath.Join(layout.StagingDir(ident.ID), "held-note.md")); err != nil {
		t.Errorf("the held file was not restored to staging: %v", err)
	}
}

// A second keep of the same id must refuse rather than mint a duplicate
// entry — and must not move anything.
func TestVerdictKeep_DuplicateRefuses(t *testing.T) {
	root, _, _, batch := verdictFixture(t)
	if err := runVerdictKeep(os.Stderr, &cobra.Command{}, root, batch, "held-note", "first"); err != nil {
		t.Fatalf("first keep: %v", err)
	}
	// Re-quarantine a copy so a second keep has a file to find.
	note := "---\nid: held-note\ntrigger: t\nconfidence: 0.9\nscope: project\n---\n\n## Action\na\n"
	if err := os.WriteFile(filepath.Join(batch, "held-note.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runVerdictKeep(os.Stderr, &cobra.Command{}, root, batch, "held-note", "second")
	if err == nil || !strings.Contains(err.Error(), "already in allow_ids") {
		t.Fatalf("duplicate keep = %v, want an already-in-allow_ids refusal", err)
	}
}

// keep with no config REFUSES: this is a manual write path into policy,
// and a file the operator never created is not theirs to have decided in.
func TestVerdictKeep_MissingConfigRefuses(t *testing.T) {
	root, _, _, batch := verdictFixture(t)
	if err := os.Remove(filepath.Join(root, ".bough.yaml")); err != nil {
		t.Fatal(err)
	}
	err := runVerdictKeep(os.Stderr, &cobra.Command{}, root, batch, "held-note", "why")
	if err == nil {
		t.Fatal("keep with no config must refuse, not mint one")
	}
	if _, serr := os.Stat(filepath.Join(batch, "held-note.md")); serr != nil {
		t.Errorf("a refused keep must not move the file: %v", serr)
	}
}

// retire appends the judgement and deletes nothing.
func TestVerdictRetire_RecordsAndKeepsTheFile(t *testing.T) {
	root, _, _, batch := verdictFixture(t)

	err := runVerdictRetire(os.Stderr, root, batch, "held-note",
		"teaches an unauthorised force-push; breaks the shared-branch rule")
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	report, err := os.ReadFile(filepath.Join(batch, "REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "retired: held-note") ||
		!strings.Contains(string(report), "unauthorised force-push") {
		t.Errorf("REPORT.md missing the retirement record:\n%s", report)
	}
	if _, err := os.Stat(filepath.Join(batch, "held-note.md")); err != nil {
		t.Errorf("retire must leave the file in quarantine: %v", err)
	}
}

// done drops the REVIEWED marker the per-prompt notice keys on.
func TestVerdictDone_MarksTheBatchReviewed(t *testing.T) {
	root, _, _, batch := verdictFixture(t)
	if err := runVerdictDone(os.Stderr, root, batch); err != nil {
		t.Fatalf("done: %v", err)
	}
	if _, err := os.Stat(filepath.Join(batch, "REVIEWED")); err != nil {
		t.Errorf("REVIEWED marker missing: %v", err)
	}
}

// With no --batch, the id is found across every batch, newest first —
// this port reviews a backlog, and "which of 42 batches" is not an error
// worth typing --batch for.
func TestVerdict_FindsTheIDAcrossBatches(t *testing.T) {
	root, layout, ident, batch := verdictFixture(t)
	older := filepath.Join(layout.QuarantineDir(ident.ID), "20251231-000000")
	if err := os.MkdirAll(older, 0o755); err != nil {
		t.Fatal(err)
	}
	note := "---\nid: old-note\ntrigger: t\nconfidence: 0.9\nscope: project\n---\n\n## Action\na\n"
	if err := os.WriteFile(filepath.Join(older, "old-note.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = batch
	if err := runVerdictRetire(os.Stderr, root, "", "old-note", "found in an older batch"); err != nil {
		t.Fatalf("retire across batches: %v", err)
	}
	report, err := os.ReadFile(filepath.Join(older, "REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "retired: old-note") {
		t.Errorf("the older batch's REPORT.md did not receive the record:\n%s", report)
	}
}
