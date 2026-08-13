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

// injectConfigOnly drops the broken-config path so the exclusion tests,
// which are about which ids are suppressed, read as one expression.
func injectConfigOnly(cmd *cobra.Command, root string) *config.Config {
	cfg, _ := injectConfig(cmd, root)
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
	if !strings.Contains(buf.String(), "does not parse") {
		t.Errorf("a broken config must be announced, got:\n%s", buf.String())
	}
}

// ...and a project with no config at all is the ordinary unconfigured case,
// which must stay a clean no-op.
func TestInjectContext_MissingConfigIsSilent(t *testing.T) {
	repo := injectFixture(t, "0.10")
	var buf bytes.Buffer
	if err := runInjectContext(&cobra.Command{}, &buf, repo, inject.Options{}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("no config is not a problem to announce, got:\n%s", buf.String())
	}
}
