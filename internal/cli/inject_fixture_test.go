package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// injectFixture stands up the smallest thing runInjectContext will act
// on — a git repo it can resolve an identity from, an isolated corpus,
// and one minted instinct at the given confidence — and returns the repo
// root. Shared by every test that drives the injector end to end.
func injectFixture(t *testing.T, confidence string) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable in this environment: %v (%s)", err, out)
		}
	}
	corpus := t.TempDir()
	t.Setenv(homunculus.DefaultDirEnv, corpus)

	ident, err := homunculus.DetectIdentity(repo)
	if err != nil {
		t.Skipf("identity resolution needs a git repo: %v", err)
	}
	layout := homunculus.NewLayout()
	if err := layout.EnsureProjectDirs(ident.ID); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: minted-note\ntrigger: when doing the thing\nconfidence: " + confidence +
		"\nscope: project\n---\n\n## Action\nDo the minted thing.\n"
	if err := os.WriteFile(filepath.Join(layout.InstinctsDir(ident.ID), "minted-note.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}
