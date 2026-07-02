package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// TestEccImport_WarnsOnOrphanProjectDir covers the #32 follow-up: a
// project dir on disk but absent from projects.json is reported, not
// silently skipped.
func TestEccImport_WarnsOnOrphanProjectDir(t *testing.T) {
	eccRoot := t.TempDir()
	writeFile(t, filepath.Join(eccRoot, "projects.json"),
		`{"reg":{"name":"p","root":"/r","remote":""}}`)
	writeFile(t, filepath.Join(eccRoot, "projects", "reg", "instincts", "personal", "a.md"), "# a")
	writeFile(t, filepath.Join(eccRoot, "projects", "orphan", "instincts", "personal", "b.md"), "# b")

	cmd := newEccImportCmd()
	cmd.SetArgs([]string{"--from", eccRoot}) // dry-run default → no homunculus writes
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "projects/orphan is on disk but not in projects.json") {
		t.Errorf("missing orphan warning:\n%s", out.String())
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestCopyProject_FollowsDedupInstinctsSymlink is the regression test for
// the v0.9.2 import bug: ECC dedups a re-keyed project by symlinking its
// instincts/ dir at the physical project that still holds the files, and
// the original copyProject skipped every symlink — so `import --apply`
// reported thousands of instincts (the count probe reads through the
// link) but copied zero.
func TestCopyProject_FollowsDedupInstinctsSymlink(t *testing.T) {
	// Arrange: physical store under "old", re-keyed "new" links to it.
	eccRoot := t.TempDir()
	oldPersonal := filepath.Join(eccRoot, "projects", "old", "instincts", "personal")
	writeFile(t, filepath.Join(oldPersonal, "alpha.md"), "# alpha\nbody")
	writeFile(t, filepath.Join(oldPersonal, "beta.md"), "# beta\nbody")

	newDir := filepath.Join(eccRoot, "projects", "new")
	writeFile(t, filepath.Join(newDir, "project.json"), `{"id":"new"}`)
	if err := os.Symlink("../old/instincts", filepath.Join(newDir, "instincts")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// The count probe must read through the link (it already did — that
	// is precisely why the bug reported a non-zero count while copying
	// nothing).
	if got := countInstincts(filepath.Join(newDir, "instincts", "personal")); got != 2 {
		t.Fatalf("countInstincts through symlink = %d, want 2", got)
	}

	// Act
	dst := filepath.Join(t.TempDir(), "new")
	if err := copyProject(newDir, dst); err != nil {
		t.Fatalf("copyProject: %v", err)
	}

	// Assert: the symlinked instincts were materialised as real files,
	// and the destination count equals the probe count (count == copy).
	for _, name := range []string{"alpha.md", "beta.md"} {
		if _, err := os.Stat(filepath.Join(dst, "instincts", "personal", name)); err != nil {
			t.Errorf("instinct %s not copied through dedup symlink: %v", name, err)
		}
	}
	if got := countInstincts(filepath.Join(dst, "instincts", "personal")); got != 2 {
		t.Errorf("dest instinct count = %d, want 2 (count must equal copy)", got)
	}
}

// TestCopyProject_CopiesRegularFilesAndToleratesDangling covers the
// normal path (plain files copied) and the abnormal path (a dangling
// symlink is skipped, not fatal, so one bad link never aborts a
// migration).
func TestCopyProject_CopiesRegularFilesAndToleratesDangling(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "project.json"), "{}")
	writeFile(t, filepath.Join(src, "instincts", "personal", "real.md"), "x")
	if err := os.Symlink("/no/such/target", filepath.Join(src, "broken")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out")
	if err := copyProject(src, dst); err != nil {
		t.Fatalf("copyProject must tolerate a dangling symlink: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "instincts", "personal", "real.md")); err != nil {
		t.Errorf("regular file not copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "broken")); !os.IsNotExist(err) {
		t.Errorf("dangling symlink should be skipped, got err=%v", err)
	}
}

// TestCopyProject_NormalizesCorruptInstinct is the import-boundary heal
// for the ECC single-line corruption (handover: bough-instinct-
// corruption). An instinct written as one physical line with literal \n
// escapes must be un-escaped on import so bough's strict reader loads it
// instead of silently dropping it.
func TestCopyProject_NormalizesCorruptInstinct(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "project.json"), "{}")
	// One physical line, literal \n throughout (backtick keeps them raw).
	corrupt := `---\nid: heal-me\nconfidence: 0.8\ndomain: workflow\n---\n\n## Action\nStay parseable.\n`
	writeFile(t, filepath.Join(src, "instincts", "personal", "heal-me.md"), corrupt)

	dst := filepath.Join(t.TempDir(), "out")
	if err := copyProject(src, dst); err != nil {
		t.Fatalf("copyProject: %v", err)
	}

	healed := filepath.Join(dst, "instincts", "personal", "heal-me.md")
	got, err := os.ReadFile(healed)
	if err != nil {
		t.Fatalf("read healed file: %v", err)
	}
	if bytes.Contains(got, []byte(`\nid:`)) {
		t.Errorf("import left the file corrupt (literal \\n):\n%s", got)
	}
	// The healed file must load through bough's strict reader.
	in, err := homunculus.ReadInstinctFile(healed)
	if err != nil {
		t.Fatalf("healed instinct does not load: %v", err)
	}
	if in.ID != "heal-me" {
		t.Errorf("healed id = %q, want heal-me", in.ID)
	}
	if !strings.Contains(in.Body, "Stay parseable.") {
		t.Errorf("healed body lost content:\n%s", in.Body)
	}
}

// TestCopyProject_FollowsFileSymlink covers ECC's file-level dedup links
// (e.g. MEMORY.md -> ../<old-id>/MEMORY.md): a symlink to a file is
// copied by value, not skipped.
func TestCopyProject_FollowsFileSymlink(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "MEMORY-real.md"), "memory body")
	if err := os.Symlink("MEMORY-real.md", filepath.Join(src, "MEMORY.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out")
	if err := copyProject(src, dst); err != nil {
		t.Fatalf("copyProject: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "MEMORY.md"))
	if err != nil {
		t.Fatalf("file symlink not copied by value: %v", err)
	}
	if string(got) != "memory body" {
		t.Errorf("file symlink content = %q, want %q", got, "memory body")
	}
}

func TestReadECCProjects(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "projects.json"),
		`{"abc":{"name":"p","root":"/r","remote":"git@x"}}`)

	got, err := readECCProjects(root)
	if err != nil {
		t.Fatalf("readECCProjects: %v", err)
	}
	if got["abc"].Name != "p" || got["abc"].Remote != "git@x" || got["abc"].Root != "/r" {
		t.Errorf("parsed = %+v", got["abc"])
	}

	// A missing projects.json is empty, not an error (an empty ECC root).
	empty, err := readECCProjects(t.TempDir())
	if err != nil {
		t.Errorf("missing projects.json should not error: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("missing projects.json should be empty, got %d", len(empty))
	}
}

// TestCountInstincts_ExcludesCatalogFiles guards the catalog-exclusion
// rule the count probe and the importer share.
func TestCountInstincts_ExcludesCatalogFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.md", "b.md", "INSTINCTS.md", "MEMORY.md", "README.md", "notes.txt"} {
		writeFile(t, filepath.Join(dir, n), "x")
	}
	if got := countInstincts(dir); got != 2 {
		t.Errorf("countInstincts = %d, want 2 (only a.md + b.md)", got)
	}
}
