package inject

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLessons(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLessonsBlock_AbsentIsCleanNoOp pins the hook contract: with no
// lessons file the block is empty, so the prompt is unaffected.
func TestLessonsBlock_AbsentIsCleanNoOp(t *testing.T) {
	if got := LessonsBlock(t.TempDir(), nil, DefaultMaxBytes); got != "" {
		t.Errorf("no lessons file should render nothing, got %q", got)
	}
}

// TestLessonsBlock_EmptyFileIsCleanNoOp pins the same for a file that
// exists but says nothing — a stub lessons file should not emit a
// dangling header claiming corrections exist.
func TestLessonsBlock_EmptyFileIsCleanNoOp(t *testing.T) {
	root := t.TempDir()
	writeLessons(t, root, "lessons.md", "\n\n   \n")
	if got := LessonsBlock(root, nil, DefaultMaxBytes); got != "" {
		t.Errorf("whitespace-only lessons file should render nothing, got %q", got)
	}
}

// TestLessonsBlock_RendersContentAndPrecedence pins what the block
// claims: it names the file and states that these corrections outrank
// the learned instincts, since that ordering is the whole reason human
// ground truth is injected separately.
func TestLessonsBlock_RendersContentAndPrecedence(t *testing.T) {
	root := t.TempDir()
	writeLessons(t, root, "tasks/lessons.md", "- Always re-read the enclosing function before editing.\n")
	got := LessonsBlock(root, nil, DefaultMaxBytes)
	for _, want := range []string{"tasks/lessons.md", "outrank", "re-read the enclosing function"} {
		if !strings.Contains(got, want) {
			t.Errorf("lessons block missing %q:\n%s", want, got)
		}
	}
}

// TestLessonsBlock_FirstExistingPathWins pins the candidate order so a
// repo with several conventional locations gets a deterministic answer.
func TestLessonsBlock_FirstExistingPathWins(t *testing.T) {
	root := t.TempDir()
	writeLessons(t, root, "tasks/lessons.md", "- from tasks\n")
	writeLessons(t, root, ".claude/lessons.md", "- from dot-claude\n")
	got := LessonsBlock(root, nil, DefaultMaxBytes)
	if !strings.Contains(got, "from dot-claude") {
		t.Errorf("expected .claude/lessons.md (first candidate) to win:\n%s", got)
	}
	if strings.Contains(got, "from tasks") {
		t.Errorf("second candidate must not also be rendered:\n%s", got)
	}
}

// TestLessonsBlock_ConfiguredPathOverridesDefaults pins the config seam
// for repos whose corrections file is not in a conventional place.
func TestLessonsBlock_ConfiguredPathOverridesDefaults(t *testing.T) {
	root := t.TempDir()
	writeLessons(t, root, "lessons.md", "- conventional\n")
	writeLessons(t, root, "docs/corrections.md", "- configured\n")
	got := LessonsBlock(root, []string{"docs/corrections.md"}, DefaultMaxBytes)
	if !strings.Contains(got, "configured") || strings.Contains(got, "conventional") {
		t.Errorf("configured path should win outright:\n%s", got)
	}
}

// TestLessonsBlock_TruncationIsStatedNotSilent pins the no-silent-caps
// invariant: an oversized lessons file is cut, but the block says so and
// names the file to read, because a quietly halved lessons file reads as
// "this is all the guidance there is".
func TestLessonsBlock_TruncationIsStatedNotSilent(t *testing.T) {
	root := t.TempDir()
	var body strings.Builder
	for i := 0; i < 400; i++ {
		body.WriteString("- a correction line that is reasonably long so the budget is exceeded\n")
	}
	writeLessons(t, root, "lessons.md", body.String())

	got := LessonsBlock(root, nil, DefaultMaxBytes)
	if !strings.Contains(got, "truncated") {
		t.Errorf("truncation must be stated in the block:\n%s", got[:min(len(got), 400)])
	}
	if !strings.Contains(got, "lessons.md") {
		t.Errorf("truncation notice must name the file to read for the rest")
	}
	// The lessons block gets its own slice of the budget so it cannot
	// starve the minted instincts that follow it.
	if len(got) > DefaultMaxBytes/lessonsBudgetFraction+512 {
		t.Errorf("lessons block = %d bytes, want ≈ budget/%d + header", len(got), lessonsBudgetFraction)
	}
}

// TestLessonsBlock_ZeroMaxBytesUsesDefaultBudget pins the defaulting
// seam. Callers hand Build an Options{} and let it default internally,
// so MaxBytes arrives here as 0; computing a fraction of 0 truncated the
// entire block away and emitted a header over an empty body.
func TestLessonsBlock_ZeroMaxBytesUsesDefaultBudget(t *testing.T) {
	root := t.TempDir()
	writeLessons(t, root, "lessons.md", "- Never skip the readiness gate.\n")
	got := LessonsBlock(root, nil, 0)
	if !strings.Contains(got, "Never skip the readiness gate") {
		t.Errorf("zero MaxBytes must fall back to the default budget, got:\n%s", got)
	}
	if strings.Contains(got, "truncated") {
		t.Errorf("a one-line lessons file must not be truncated:\n%s", got)
	}
}

// TestTruncateAtLineBoundary pins that a cut never lands mid-line: a
// correction sliced after "never run" inverts its own meaning.
func TestTruncateAtLineBoundary(t *testing.T) {
	s := "- never force-push a shared branch\n- never merge unasked\n"
	got := truncateAtLineBoundary(s, 40)
	if strings.HasSuffix(got, "never") || strings.Contains(got, "never merge un") {
		t.Errorf("cut landed mid-line: %q", got)
	}
	if !strings.HasSuffix(got, "shared branch") {
		t.Errorf("expected the cut at the line boundary, got %q", got)
	}
	if got := truncateAtLineBoundary("short", 100); got != "short" {
		t.Errorf("under-budget input must pass through unchanged, got %q", got)
	}
}
