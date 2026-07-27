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

// twoInstinctResult builds a parsed result with one benign instinct and
// one whose action is a command-shaped forbidden action (merge unasked).
func twoInstinctResult() map[string]any {
	return map[string]any{
		"instincts": []any{
			map[string]any{
				"id":         "read-before-edit",
				"trigger":    "when editing unfamiliar files",
				"confidence": 0.7,
				"domain":     "workflow",
				"scope":      "project",
				"action":     "Read the surrounding implementation before editing.",
				"evidence":   []any{"observed 5 times"},
			},
			map[string]any{
				"id":         "merge-when-green",
				"trigger":    "when CI is green on an approved PR",
				"confidence": 0.7,
				"domain":     "workflow",
				"scope":      "project",
				"action":     "Run `gh pr merge --squash` to land it.",
				"evidence":   []any{"observed once"},
			},
		},
	}
}

func stagedFromResult(t *testing.T, layout homunculus.Layout, ident homunculus.ProjectIdentity, now time.Time) []stagedInstinct {
	t.Helper()
	staged, skipped, errs := stageInstincts(layout.StagingDir(ident.ID), ident, twoInstinctResult(), now)
	if skipped != 0 || len(errs) != 0 {
		t.Fatalf("stageInstincts: skipped=%d errs=%v, want 0/none", skipped, errs)
	}
	if len(staged) != 2 {
		t.Fatalf("staged = %d, want 2", len(staged))
	}
	return staged
}

// TestStageInstincts_IsInvisibleToInjection is the structural claim: a
// staged instinct is written to disk but a scan of the personal corpus
// (what injection reads) does not see it — the live-before-check window
// is closed by layout, not by timing.
func TestStageInstincts_IsInvisibleToInjection(t *testing.T) {
	layout := homunculus.FromRoot(t.TempDir())
	ident := homunculus.ProjectIdentity{ID: "proj1", Name: "demo"}
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)

	staged := stagedFromResult(t, layout, ident, now)

	// The staged files exist on disk under the staging dir …
	for _, s := range staged {
		if _, err := os.Stat(s.path); err != nil {
			t.Errorf("staged file missing: %v", err)
		}
		if !strings.Contains(s.path, string(filepath.Separator)+".staging"+string(filepath.Separator)) {
			t.Errorf("staged path %q is not under .staging", s.path)
		}
	}
	// … yet injection (which scans the personal dir) sees nothing.
	got, _ := homunculus.ScanInstincts(layout.InstinctsDir(ident.ID))
	if len(got) != 0 {
		t.Errorf("personal corpus has %d instincts before the gate ran, want 0", len(got))
	}
}

// TestScreenAndPromote_ClearedPromoted_HeldQuarantined is the end-to-end
// placement test: with the gate enabled the benign note lands in the
// personal corpus (injectable) and the forbidden one lands in a
// quarantine batch with a REPORT — never in personal, never deleted.
func TestScreenAndPromote_ClearedPromoted_HeldQuarantined(t *testing.T) {
	layout := homunculus.FromRoot(t.TempDir())
	ident := homunculus.ProjectIdentity{ID: "proj1", Name: "demo"}
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	staged := stagedFromResult(t, layout, ident, now)

	gate := instinctgate.New(instinctgate.Config{Enabled: true})
	out := screenAndPromote(layout, ident.ID, staged, gate, now)

	if out.Emitted != 1 || out.Quarantined != 1 {
		t.Fatalf("emitted=%d quarantined=%d, want 1/1 (errs=%v)", out.Emitted, out.Quarantined, out.Errs)
	}
	if len(out.Errs) != 0 {
		t.Fatalf("unexpected errs: %v", out.Errs)
	}

	// Personal corpus: exactly the benign one, and injection can see it.
	personal, _ := homunculus.ScanInstincts(layout.InstinctsDir(ident.ID))
	if len(personal) != 1 || personal[0].ID != "read-before-edit" {
		t.Errorf("personal corpus = %+v, want only read-before-edit", personal)
	}

	// The forbidden one is NOT in personal, but its file still exists in
	// the quarantine batch (moved, not deleted).
	forbidden := filepath.Join(layout.InstinctsDir(ident.ID), "merge-when-green.md")
	if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
		t.Errorf("forbidden instinct leaked into personal corpus (stat err=%v)", err)
	}
	if out.BatchDir == "" {
		t.Fatal("BatchDir empty despite a quarantined instinct")
	}
	held := filepath.Join(out.BatchDir, "merge-when-green.md")
	if _, err := os.Stat(held); err != nil {
		t.Errorf("quarantined file missing (should be moved, not deleted): %v", err)
	}

	// Staging is drained — nothing left live-before-check.
	entries, _ := os.ReadDir(layout.StagingDir(ident.ID))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".md" {
			t.Errorf("staging leftover after promote: %s", e.Name())
		}
	}
}

// TestQuarantineReport_HasReversibleRestore pins the reversibility
// contract: the REPORT names the rule and prints a copy-paste restore
// command that moves the held file back into the personal corpus.
func TestQuarantineReport_HasReversibleRestore(t *testing.T) {
	layout := homunculus.FromRoot(t.TempDir())
	ident := homunculus.ProjectIdentity{ID: "proj1", Name: "demo"}
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	staged := stagedFromResult(t, layout, ident, now)

	gate := instinctgate.New(instinctgate.Config{Enabled: true})
	out := screenAndPromote(layout, ident.ID, staged, gate, now)

	report, err := os.ReadFile(filepath.Join(out.BatchDir, "REPORT.md"))
	if err != nil {
		t.Fatalf("REPORT.md missing: %v", err)
	}
	s := string(report)
	for _, want := range []string{
		"never-merge-unasked",                  // the rule that held it
		"mv ",                                  // a restore command
		"merge-when-green.md",                  // the held file
		filepath.Join("instincts", "personal"), // restore target is the personal corpus
		"COMMAND SHAPES ONLY",                  // honest scope banner (no completeness claim)
	} {
		if !strings.Contains(s, want) {
			t.Errorf("REPORT.md missing %q\n---\n%s", want, s)
		}
	}
}

// TestScreenAndPromote_GateDisabled_AllPromoted pins the safe default:
// a disabled gate is byte-for-byte the pre-gate behaviour — every staged
// instinct is promoted, nothing is quarantined.
func TestScreenAndPromote_GateDisabled_AllPromoted(t *testing.T) {
	layout := homunculus.FromRoot(t.TempDir())
	ident := homunculus.ProjectIdentity{ID: "proj1", Name: "demo"}
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	staged := stagedFromResult(t, layout, ident, now)

	gate := instinctgate.New(instinctgate.Config{Enabled: false})
	out := screenAndPromote(layout, ident.ID, staged, gate, now)

	if out.Emitted != 2 || out.Quarantined != 0 {
		t.Fatalf("emitted=%d quarantined=%d, want 2/0", out.Emitted, out.Quarantined)
	}
	personal, _ := homunculus.ScanInstincts(layout.InstinctsDir(ident.ID))
	if len(personal) != 2 {
		t.Errorf("personal corpus = %d, want 2 (gate off promotes all)", len(personal))
	}
	if _, err := os.Stat(layout.QuarantineDir(ident.ID)); !os.IsNotExist(err) {
		t.Errorf("quarantine dir created despite gate off (err=%v)", err)
	}
}
