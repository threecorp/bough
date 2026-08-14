package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/inject"
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

// TestInjectContext_NothingToSayIsCleanNoOp pins the hook contract: with
// nothing to say, stdout stays byte-empty so the prompt is completely
// unaffected. It lived beside the lessons tests and is not about lessons —
// it is the only assertion that the UserPromptSubmit hook can stay silent.
func TestInjectContext_NothingToSayIsCleanNoOp(t *testing.T) {
	repo := injectFixture(t, "0.10") // below the confidence floor
	var buf bytes.Buffer
	if err := runInjectContext(&cobra.Command{}, &buf, repo, inject.Options{}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected a clean no-op, got:\n%s", buf.String())
	}
}

// TestInjectContext_OffTopicPromptInjectsNothing pins the drop rule at the
// CLI boundary: a prompt matching no instinct must produce an empty block,
// not the corpus's alphabetical head.
func TestInjectContext_OffTopicPromptInjectsNothing(t *testing.T) {
	repo := injectFixture(t, "0.9")
	var buf bytes.Buffer
	opts := inject.Options{Prompt: "photosynthesis in alpine wildflowers"}
	if err := runInjectContext(&cobra.Command{}, &buf, repo, opts); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("off-topic prompt injected something:\n%s", buf.String())
	}
}

// injectConfigOnly keeps the exclusion tests reading as one expression
// while refusing to hide a fixture whose config stopped loading. Those
// tests assert an EMPTY exclusion set, which a nil config also produces —
// so without this the fixture could rot and they would still pass.
func injectConfigOnly(t *testing.T, cmd *cobra.Command, root string) *config.Config {
	t.Helper()
	cfg, failure := injectConfig(cmd, root)
	if failure != "" {
		t.Fatalf("the fixture's config must load, or this test proves nothing: %s", failure)
	}
	return cfg
}

// A config file that exists but will not parse must SAY so in the block.
// Degrading to defaults is right on the prompt path, but doing it silently
// turns the operator's exclusion register and alias file off with no symptom
// other than muted instincts quietly returning.
func TestInjectContext_UnparseableConfigIsAnnounced(t *testing.T) {
	repo := injectFixture(t, "0.9")
	if err := os.WriteFile(filepath.Join(repo, ".bough.yaml"), []byte("schema_version: 2\nnope: {{\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := runInjectContext(&cobra.Command{}, &buf, repo, inject.Options{}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if !strings.Contains(buf.String(), "config not loaded") {
		t.Errorf("a broken config must be announced, got:\n%s", buf.String())
	}
}

// ...and a project with no config at all is the ordinary unconfigured
// case, which must add no notice. Asserted against a prompt that DOES
// select, so the block is non-empty and the absence of the notice is the
// only thing this can be passing on.
func TestInjectContext_MissingConfigAddsNoNotice(t *testing.T) {
	repo := injectFixture(t, "0.9")
	if _, err := os.Stat(filepath.Join(repo, ".bough.yaml")); !os.IsNotExist(err) {
		t.Fatalf("this fixture must have no config for the case to be the one named")
	}
	var buf bytes.Buffer
	opts := inject.Options{Prompt: "do the minted thing"}
	if err := runInjectContext(&cobra.Command{}, &buf, repo, opts); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "Do the minted thing") {
		t.Fatalf("the fixture must inject something, or the assertion below is vacuous:\n%s", out)
	}
	if strings.Contains(buf.String(), "config not loaded") {
		t.Errorf("an absent config is not a fault to announce:\n%s", buf.String())
	}
}

// An explicitly NAMED config that is not there IS a fault: the operator
// asked for that file by name, so silence would be the same silent
// degradation this notice exists to end.
func TestInjectContext_NamedButMissingConfigIsAnnounced(t *testing.T) {
	repo := injectFixture(t, "0.9")
	t.Setenv("BOUGH_CONFIG", filepath.Join(repo, "typo.yaml"))
	var buf bytes.Buffer
	if err := runInjectContext(&cobra.Command{}, &buf, repo, inject.Options{}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if !strings.Contains(buf.String(), "config not loaded") {
		t.Errorf("a config named by the operator and absent must be announced:\n%s", buf.String())
	}
}

// TestInjectContext_PromptSelectsRelevantInstinct pins the plumbing
// end-to-end: the prompt has to reach selection, or relevance ranking is
// dead code. Two instincts, one matching the prompt — only that one may
// be injected. It went with the lessons test file and was never about
// lessons.
func TestInjectContext_PromptSelectsRelevantInstinct(t *testing.T) {
	repo := injectFixture(t, "0.9") // seeds minted-note ("Do the minted thing")
	ident, err := homunculus.DetectIdentity(repo)
	if err != nil {
		t.Skipf("identity: %v", err)
	}
	other := "---\nid: mysql-handshake\ntrigger: when the mysql probe fails\nconfidence: 0.9\nscope: project\n---\n\n" +
		"## Action\nRetry the mysql handshake for 30 seconds.\n"
	dir := homunculus.NewLayout().InstinctsDir(ident.ID)
	if err := os.WriteFile(filepath.Join(dir, "mysql-handshake.md"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	opts := inject.Options{Prompt: "the mysql handshake keeps failing"}
	if err := runInjectContext(&cobra.Command{}, &buf, repo, opts); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Retry the mysql handshake") {
		t.Errorf("the prompt-relevant instinct was not injected:\n%s", out)
	}
	if strings.Contains(out, "Do the minted thing") {
		t.Errorf("an unrelated instinct was injected despite the prompt:\n%s", out)
	}
}
