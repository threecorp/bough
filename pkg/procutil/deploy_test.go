package procutil

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// TestDeployAssets_MaterialisesAndOverwrites feeds an in-memory fs.FS
// (embed.FS satisfies the same interface) and asserts every file lands
// byte-exact with its directory tree, and that a re-run overwrites in
// place — the idempotency a skills / commands upgrade relies on.
func TestDeployAssets_MaterialisesAndOverwrites(t *testing.T) {
	assets := fstest.MapFS{
		"skills/using-bough/SKILL.md": {Data: []byte("skill contents")},
		"skills/nested/dir/NOTE.md":   {Data: []byte("nested note")},
	}
	dst := t.TempDir()

	if err := DeployAssets(assets, "skills", dst); err != nil {
		t.Fatalf("DeployAssets: %v", err)
	}
	assertFileContent(t, filepath.Join(dst, "using-bough", "SKILL.md"), "skill contents")
	assertFileContent(t, filepath.Join(dst, "nested", "dir", "NOTE.md"), "nested note")

	// Mutate a materialised file, re-run, and the embedded content must
	// be restored.
	if err := os.WriteFile(filepath.Join(dst, "using-bough", "SKILL.md"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if err := DeployAssets(assets, "skills", dst); err != nil {
		t.Fatalf("DeployAssets (2nd run): %v", err)
	}
	assertFileContent(t, filepath.Join(dst, "using-bough", "SKILL.md"), "skill contents")
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
}
