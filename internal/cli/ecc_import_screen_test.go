package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/homunculus"
	"github.com/ikeikeikeike/bough/internal/instinctgate"
)

// importFixture builds a foreign corpus holding one benign and one
// forbidden instinct, plus a non-instinct file, and returns the source
// dir with a screen aimed at a fresh destination.
func importFixture(t *testing.T) (src string, layout homunculus.Layout, screen *importScreen) {
	t.Helper()
	src = t.TempDir()
	personal := filepath.Join(src, "instincts", "personal")
	if err := os.MkdirAll(personal, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(dir, id, trigger, action string) {
		body := "---\nid: " + id + "\ntrigger: " + trigger + "\nconfidence: 0.9\nscope: project\n---\n\n" +
			"## Action\n" + action + "\n"
		if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(personal, "read-before-edit", "when editing unfamiliar files",
		"Read the surrounding implementation before editing.")
	write(personal, "merge-when-green", "when CI is green on an approved PR",
		"Run `gh pr merge --squash` to land it.")
	// A note whose forbidden line is the SECOND one: the mint path screens
	// the whole action block, so import must too.
	write(personal, "ship-after-review", "when a reviewed change is ready",
		"Confirm the reviewer signed off.\nThen run `gh pr merge --squash` to land it.")
	// Not an instinct: a catalog file, and a file outside the tree.
	if err := os.WriteFile(filepath.Join(personal, "MEMORY.md"), []byte("# catalog\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "notes.md"), []byte("# not an instinct\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	layout = homunculus.FromRoot(t.TempDir())
	screen = &importScreen{
		gate:      instinctgate.New(instinctgate.Config{Enabled: true}),
		layout:    layout,
		now:       time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
		projectID: "proj1",
	}
	return src, layout, screen
}

// The hole this closes: import wrote a foreign corpus straight into
// instincts/personal, so a note recommending an unasked merge was
// injectable the moment the copy finished.
func TestImportScreensInstinctsIntoQuarantine(t *testing.T) {
	src, layout, screen := importFixture(t)

	if err := copyProject(src, layout.ProjectDir("proj1"), screen); err != nil {
		t.Fatalf("copyProject: %v", err)
	}
	scanned, held, batch, err := screen.flush()
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if scanned != 3 || held != 2 {
		t.Errorf("scanned=%d held=%d, want 3 scanned / 2 held", scanned, held)
	}

	// The benign note is injectable; neither forbidden one is.
	got, _ := homunculus.ScanInstincts(layout.InstinctsDir("proj1"))
	var ids []string
	for _, in := range got {
		ids = append(ids, in.ID)
	}
	if len(ids) != 1 || ids[0] != "read-before-edit" {
		t.Errorf("corpus = %v, want only read-before-edit", ids)
	}

	// Held notes are reviewable, not dropped.
	for _, id := range []string{"merge-when-green", "ship-after-review"} {
		if _, err := os.Stat(filepath.Join(batch, id+".md")); err != nil {
			t.Errorf("%s is not in quarantine: %v", id, err)
		}
	}
	report, rerr := os.ReadFile(filepath.Join(batch, "REPORT.md"))
	if rerr != nil {
		t.Fatalf("REPORT.md: %v", rerr)
	}
	for _, want := range []string{"merge-when-green", "never-merge-unasked", ".staging"} {
		if !strings.Contains(string(report), want) {
			t.Errorf("REPORT.md missing %q:\n%s", want, report)
		}
	}
}

// The source corpus is never modified — import copies, and a refusal
// must not reach back into somebody else's files.
func TestImportLeavesTheSourceCorpusUntouched(t *testing.T) {
	src, layout, screen := importFixture(t)
	before, err := os.ReadDir(filepath.Join(src, "instincts", "personal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := copyProject(src, layout.ProjectDir("proj1"), screen); err != nil {
		t.Fatalf("copyProject: %v", err)
	}
	if _, _, _, ferr := screen.flush(); ferr != nil {
		t.Fatalf("flush: %v", ferr)
	}
	after, err := os.ReadDir(filepath.Join(src, "instincts", "personal"))
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Errorf("source file count changed: %d → %d", len(before), len(after))
	}
}

// A refused replacement must never destroy a good file already at the
// destination. This is the trap the reference hit: it computed the
// stale-file list BEFORE screening, so a refused import deleted the
// existing correct version and wrote nothing back.
func TestImportRefusalDoesNotDestroyAnExistingGoodFile(t *testing.T) {
	src, layout, screen := importFixture(t)
	dstPersonal := layout.InstinctsDir("proj1")
	if err := os.MkdirAll(dstPersonal, 0o755); err != nil {
		t.Fatal(err)
	}
	good := "---\nid: merge-when-green\ntrigger: when a merge was explicitly requested\n" +
		"confidence: 0.9\nscope: project\n---\n\n## Action\nAsk first; merge only on an explicit instruction.\n"
	target := filepath.Join(dstPersonal, "merge-when-green.md")
	if err := os.WriteFile(target, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copyProject(src, layout.ProjectDir("proj1"), screen); err != nil {
		t.Fatalf("copyProject: %v", err)
	}
	if _, _, _, ferr := screen.flush(); ferr != nil {
		t.Fatalf("flush: %v", ferr)
	}

	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the pre-existing good file was destroyed: %v", err)
	}
	if string(after) != good {
		t.Errorf("the pre-existing good file was overwritten:\n%s", after)
	}
}

// Coverage is directory AND extension: a sweep that checks one and not
// the other is how the widest-scope writer went unscanned.
func TestScreensInstinctCoverage(t *testing.T) {
	inScope := []string{
		"instincts/personal/x.md",
		"instincts/x.md",
		"instincts/personal/nested/x.md",
	}
	for _, p := range inScope {
		if !screensInstinct(p) {
			t.Errorf("%q should be screened", p)
		}
	}
	outOfScope := []string{
		"instincts/personal/MEMORY.md",    // catalog
		"instincts/personal/INSTINCTS.md", // catalog
		"instincts/personal/x.yaml",       // not the extension ScanInstincts reads
		"telemetry/events.jsonl",          // not the instincts tree
		"notes.md",                        // .md, but outside instincts/
	}
	for _, p := range outOfScope {
		if screensInstinct(p) {
			t.Errorf("%q should NOT be screened", p)
		}
	}
}

// A nil gate means the screen could not be built. Import runs rarely,
// by hand, and writes into what every later prompt reads — so nothing
// may pass unscreened. The command builds the gate before the loop and
// fails closed on a config it cannot read; this pins that a screen with
// no gate screens nothing rather than silently clearing.
func TestImportScreenWithNoGateScreensNothing(t *testing.T) {
	src, layout, _ := importFixture(t)
	var nilScreen *importScreen
	if err := copyProject(src, layout.ProjectDir("proj1"), nilScreen); err != nil {
		t.Fatalf("copyProject: %v", err)
	}
	scanned, held, _, err := nilScreen.flush()
	if err != nil || scanned != 0 || held != 0 {
		t.Errorf("flush on a nil screen = %d/%d/%v, want 0/0/nil", scanned, held, err)
	}
}
