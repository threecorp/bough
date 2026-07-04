package procutil

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// TestDeployFlake_MaterialisesAndOverwrites feeds an in-memory fs.FS
// (embed.FS satisfies the same interface) and asserts every file lands
// byte-exact with its directory tree, and that a re-run overwrites in
// place — the idempotency a plugin upgrade relies on.
func TestDeployFlake_MaterialisesAndOverwrites(t *testing.T) {
	assets := fstest.MapFS{
		"nix/flake.nix":     {Data: []byte("flake contents")},
		"nix/mod/redis.nix": {Data: []byte("redis module")},
	}
	dst := t.TempDir()

	if err := DeployFlake(assets, "nix", dst); err != nil {
		t.Fatalf("DeployFlake: %v", err)
	}
	assertFileContent(t, filepath.Join(dst, "flake.nix"), "flake contents")
	assertFileContent(t, filepath.Join(dst, "mod", "redis.nix"), "redis module")

	// Mutate a materialised file, re-run, and the embedded content must
	// be restored.
	if err := os.WriteFile(filepath.Join(dst, "flake.nix"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if err := DeployFlake(assets, "nix", dst); err != nil {
		t.Fatalf("DeployFlake (2nd run): %v", err)
	}
	assertFileContent(t, filepath.Join(dst, "flake.nix"), "flake contents")
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
