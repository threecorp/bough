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

// TestEccImport_ApplyFalseIsExplicitDryRun is the regression guard for
// the wave-4 review finding: pflag's BoolFunc passes --apply=false's
// literal string value to the registered callback, but the original
// callback ignored the argument and unconditionally set dryRun=false
// — so --apply=false silently performed the real copy anyway.
func TestEccImport_ApplyFalseIsExplicitDryRun(t *testing.T) {
	t.Setenv("BOUGH_HOMUNCULUS_DIR", t.TempDir())
	eccRoot := t.TempDir()
	writeFile(t, filepath.Join(eccRoot, "projects.json"),
		`{"p1":{"name":"proj1","root":"/r1","remote":""}}`)
	writeFile(t, filepath.Join(eccRoot, "projects", "p1", "instincts", "personal", "a.md"), "# a")

	cmd := newEccImportCmd()
	cmd.SetArgs([]string{"--from", eccRoot, "--apply=false"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "dry-run: nothing copied") {
		t.Errorf("--apply=false did not stay a dry run:\n%s", out.String())
	}
	dst := homunculus.NewLayout()
	if _, err := os.Stat(dst.ProjectDir("p1")); err == nil {
		t.Errorf("--apply=false copied project p1 into %s", dst.ProjectDir("p1"))
	}
}

// TestEccImport_ContinuesAfterOneProjectFails is the regression guard
// for the wave-4 review finding: a copy failure for one project used
// to abort the whole `--apply` run via an immediate return, so which
// project failed (and which ones were never attempted) depended on Go's
// randomized map iteration order. Import must instead attempt every
// project regardless of an earlier failure, and report a non-zero
// exit with a summary of exactly what failed.
func TestEccImport_ContinuesAfterOneProjectFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: unreadable-file permission bits are not enforced")
	}
	t.Setenv("BOUGH_HOMUNCULUS_DIR", t.TempDir())
	eccRoot := t.TempDir()
	writeFile(t, filepath.Join(eccRoot, "projects.json"),
		`{"good":{"name":"good","root":"/g","remote":""},"bad":{"name":"bad","root":"/b","remote":""}}`)
	writeFile(t, filepath.Join(eccRoot, "projects", "good", "instincts", "personal", "a.md"), "# a")
	// "bad"'s only file is unreadable: copyFile's os.Open fails on it,
	// simulating a permission-denied file in a messy real corpus.
	badFile := filepath.Join(eccRoot, "projects", "bad", "instincts", "personal", "x.md")
	writeFile(t, badFile, "# x")
	if err := os.Chmod(badFile, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(badFile, 0o644) }) // let t.TempDir() clean up

	cmd := newEccImportCmd()
	cmd.SetArgs([]string{"--from", eccRoot, "--apply"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected a non-nil error reporting the failed project")
	}
	if !strings.Contains(out.String(), "imported 1 of 2 projects") {
		t.Errorf("missing partial-success summary:\n%s", out.String())
	}
	// The good project must still have been imported despite bad's
	// failure — this is the actual "continues past one failure" check,
	// not just the summary text.
	dst := homunculus.NewLayout()
	if _, statErr := os.Stat(filepath.Join(dst.ProjectDir("good"), "instincts", "personal", "a.md")); statErr != nil {
		t.Errorf("good project was not imported despite bad's failure: %v", statErr)
	}
	reg, regErr := homunculus.NewRegistryRW(dst).Read()
	if regErr != nil {
		t.Fatalf("read registry: %v", regErr)
	}
	if _, ok := reg["good"]; !ok {
		t.Errorf("good project was not registered in projects.json")
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
	if err := copyProject(newDir, dst, nil); err != nil {
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
	if err := copyProject(src, dst, nil); err != nil {
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
	if err := copyProject(src, dst, nil); err != nil {
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
	if err := copyProject(src, dst, nil); err != nil {
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

// TestEccImport_UnresolvableHomeSurfacesRealCause is the regression
// guard for the fsutil.ExpandHome/ExpandHomeStrict unification: ecc
// import's own RunE used to propagate a UserHomeDir failure directly;
// after unifying on the shared ExpandHome (which never errors), a
// "~"-prefixed --from with $HOME unset silently continued with the
// literal, un-expanded path and failed later with a generic "ECC root
// not found at ~/..." instead of the real cause. RunE must use the
// strict variant and surface that cause.
func TestEccImport_UnresolvableHomeSurfacesRealCause(t *testing.T) {
	t.Setenv("HOME", "")
	cmd := newEccImportCmd()
	cmd.SetArgs([]string{"--from", "~/some-ecc-root"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error when $HOME is unset and --from starts with ~")
	}
	if strings.Contains(err.Error(), "ECC root not found") {
		t.Errorf("error masked the real UserHomeDir cause behind a generic not-found message: %v", err)
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
