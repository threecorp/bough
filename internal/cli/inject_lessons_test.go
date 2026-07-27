package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/inject"
)

// injectFixture builds a throwaway git repo (identity resolution needs
// one) with its own homunculus root, seeds one minted instinct, and
// returns the repo root. Both the corpus and the repo are per-test temp
// dirs, so nothing touches the operator's real corpus.
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

// TestInjectContext_LessonsPrecedeMintedInstincts pins the delivery
// order end-to-end: human corrections are emitted ABOVE the minted
// block, because that ordering is the entire reason they are injected
// separately instead of being merged into the confidence ranking.
func TestInjectContext_LessonsPrecedeMintedInstincts(t *testing.T) {
	repo := injectFixture(t, "0.9")
	if err := os.WriteFile(filepath.Join(repo, "lessons.md"), []byte("- Never skip the readiness gate.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runInjectContext(&buf, repo, inject.Options{}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	out := buf.String()
	lessonsAt := strings.Index(out, "Never skip the readiness gate")
	mintedAt := strings.Index(out, "Do the minted thing")
	if lessonsAt < 0 || mintedAt < 0 {
		t.Fatalf("expected both blocks, got:\n%s", out)
	}
	if lessonsAt > mintedAt {
		t.Errorf("lessons must precede minted instincts:\n%s", out)
	}
}

// TestInjectContext_LessonsSurviveWhenNothingClearsTheFloor pins that
// human ground truth is not gated on the corpus: a below-floor corpus
// emits no instincts, but the corrections still reach the prompt.
func TestInjectContext_LessonsSurviveWhenNothingClearsTheFloor(t *testing.T) {
	repo := injectFixture(t, "0.10") // below the 0.50 injection floor
	if err := os.WriteFile(filepath.Join(repo, "lessons.md"), []byte("- Ground truth still applies.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runInjectContext(&buf, repo, inject.Options{}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Ground truth still applies") {
		t.Errorf("lessons must be emitted even when no instinct clears the floor:\n%s", out)
	}
	if strings.Contains(out, "Do the minted thing") {
		t.Errorf("below-floor instinct leaked into the block:\n%s", out)
	}
}

// TestInjectContext_NoLessonsNoInstinctsIsCleanNoOp pins the hook
// contract: with nothing to say, stdout stays empty so the prompt is
// completely unaffected.
func TestInjectContext_NoLessonsNoInstinctsIsCleanNoOp(t *testing.T) {
	repo := injectFixture(t, "0.10") // below floor, and no lessons file
	var buf bytes.Buffer
	if err := runInjectContext(&buf, repo, inject.Options{}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected a clean no-op, got:\n%s", buf.String())
	}
}
