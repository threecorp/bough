package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/inject"
)

// toolLine builds one transcript line the way the host writes it.
func toolLine(tool, path string) string {
	return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"` + tool +
		`","input":{"file_path":"` + path + `"}}]}}`
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The files a session just opened are the signal the prompt usually
// lacks, and "recent" has to mean recent: newest first, deduplicated.
func TestRecentFilesReadsNewestFirst(t *testing.T) {
	p := writeTranscript(t,
		toolLine("Read", "/repo/old.go"),
		toolLine("Read", "/repo/middle.go"),
		toolLine("Edit", "/repo/newest.go"),
	)
	got := newTranscriptReader().recentFiles(p)
	want := []string{"/repo/newest.go", "/repo/middle.go", "/repo/old.go"}
	if len(got) != len(want) {
		t.Fatalf("recentFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestRecentFilesDeduplicates(t *testing.T) {
	p := writeTranscript(t,
		toolLine("Read", "/repo/a.go"),
		toolLine("Read", "/repo/b.go"),
		toolLine("Edit", "/repo/a.go"),
	)
	got := newTranscriptReader().recentFiles(p)
	if len(got) != 2 || got[0] != "/repo/a.go" {
		t.Errorf("recentFiles = %v, want a.go (most recent) then b.go", got)
	}
}

// A Bash command that mentions a file is not the same evidence as
// opening it, and only the file tools carry file_path in this shape.
func TestRecentFilesIgnoresOtherToolsAndProse(t *testing.T) {
	p := writeTranscript(t,
		`{"type":"user","message":{"content":"just prose, no tools"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"thinking about /repo/never.go"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cat /repo/never.go"}}]}}`,
		toolLine("Write", "/repo/real.go"),
	)
	got := newTranscriptReader().recentFiles(p)
	if len(got) != 1 || got[0] != "/repo/real.go" {
		t.Errorf("recentFiles = %v, want only /repo/real.go", got)
	}
}

// The schema is the host's, not a contract with bough: a line that does
// not parse must cost this signal, never the turn.
func TestRecentFilesSurvivesMalformedLines(t *testing.T) {
	p := writeTranscript(t,
		"{not json at all",
		"",
		`{"message":{"content":"a string where a list belongs"}}`,
		toolLine("Read", "/repo/survivor.go"),
	)
	got := newTranscriptReader().recentFiles(p)
	if len(got) != 1 || got[0] != "/repo/survivor.go" {
		t.Errorf("recentFiles = %v, want /repo/survivor.go", got)
	}
}

func TestRecentFilesRespectsItsLimits(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, toolLine("Read", filepath.Join("/repo", string(rune('a'+i%26))+".go")))
	}
	r := newTranscriptReader()
	r.maxFiles = 3
	if got := r.recentFiles(writeTranscript(t, lines...)); len(got) != 3 {
		t.Errorf("maxFiles ignored: got %d paths (%v)", len(got), got)
	}
	// Only the TAIL of a long session is about now: with a tiny byte
	// budget the early lines must be out of reach.
	r = newTranscriptReader()
	r.tailBytes = 200
	got := r.recentFiles(writeTranscript(t, append([]string{toolLine("Read", "/repo/ancient.go")}, lines...)...))
	for _, p := range got {
		if p == "/repo/ancient.go" {
			t.Errorf("a path outside the byte tail was returned: %v", got)
		}
	}
}

func TestRecentFilesMissingTranscriptIsNoSignal(t *testing.T) {
	if got := newTranscriptReader().recentFiles(filepath.Join(t.TempDir(), "absent.jsonl")); got != nil {
		t.Errorf("a missing transcript returned %v, want nil", got)
	}
	if got := newTranscriptReader().recentFiles(""); got != nil {
		t.Errorf("an empty path returned %v, want nil", got)
	}
}

// extractTranscriptPath is the seam between the host payload and the
// reader above: if it silently returns "", the whole signal is inert.
func TestExtractTranscriptPath(t *testing.T) {
	if got := extractTranscriptPath([]byte(`{"prompt":"hi","transcript_path":"/tmp/s.jsonl"}`)); got != "/tmp/s.jsonl" {
		t.Errorf("extractTranscriptPath = %q, want /tmp/s.jsonl", got)
	}
	if got := extractTranscriptPath([]byte(`{"prompt":"hi"}`)); got != "" {
		t.Errorf("a payload without the field returned %q", got)
	}
	if got := extractTranscriptPath([]byte("not json")); got != "" {
		t.Errorf("an unparseable payload returned %q", got)
	}
}

// TestInjectContext_RecentFilesReachSelection is the end-to-end half:
// extracting paths is worthless if they never reach the ranker. The prompt
// here names NOTHING — the shape the signal exists for — so the only way
// the note can arrive is through the file the session just opened.
func TestInjectContext_RecentFilesReachSelection(t *testing.T) {
	repo := injectFixture(t, "0.9")
	ident, err := homunculus.DetectIdentity(repo)
	if err != nil {
		t.Skipf("identity resolution needs a git repo: %v", err)
	}
	layout := homunculus.NewLayout()
	body := "---\nid: layout-note\ntrigger: when touching pkg/homunculus/layout.go\nconfidence: 0.9\nscope: project\n---\n\n## Action\nRun EnsureProjectDirs first.\n"
	if err := os.WriteFile(filepath.Join(layout.InstinctsDir(ident.ID), "layout-note.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	const vague = "why is this failing"
	var buf bytes.Buffer
	if err := runInjectContext(&buf, repo, inject.Options{Prompt: vague}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if strings.Contains(buf.String(), "EnsureProjectDirs") {
		t.Fatalf("precondition: a prompt naming nothing should retrieve nothing:\n%s", buf.String())
	}

	buf.Reset()
	if err := runInjectContext(&buf, repo, inject.Options{
		Prompt:      vague,
		RecentFiles: []string{filepath.Join(repo, "pkg/homunculus/layout.go")},
	}); err != nil {
		t.Fatalf("runInjectContext: %v", err)
	}
	if !strings.Contains(buf.String(), "EnsureProjectDirs") {
		t.Errorf("the file the session just opened did not reach selection:\n%s", buf.String())
	}
}
