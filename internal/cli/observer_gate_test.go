package cli

import (
	"bytes"
	"context"
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
	out := screenAndPromote(context.Background(), layout, ident.ID, staged, gate, nil, now)

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
	out := screenAndPromote(context.Background(), layout, ident.ID, staged, gate, nil, now)

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

// TestPromote_ArchivesSupersededVersion is the archive-never-delete
// contract. Minting is id-addressed and the writer overwrites in place,
// so a re-mint under the same id would destroy the prior text with no
// record. The prior version must survive, whole, with a restore line.
func TestPromote_ArchivesSupersededVersion(t *testing.T) {
	layout := homunculus.FromRoot(t.TempDir())
	ident := homunculus.ProjectIdentity{ID: "proj1", Name: "demo"}
	gate := instinctgate.New(instinctgate.Config{Enabled: true})

	// Pass 1 mints the original.
	first := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	staged1, _, _ := stageInstincts(layout.StagingDir(ident.ID), ident, twoInstinctResult(), first)
	if out := screenAndPromote(context.Background(), layout, ident.ID, staged1, gate, nil, first); out.Superseded != 0 {
		t.Fatalf("first pass Superseded = %d, want 0 (nothing to supersede)", out.Superseded)
	}
	original, err := os.ReadFile(filepath.Join(layout.InstinctsDir(ident.ID), "read-before-edit.md"))
	if err != nil {
		t.Fatalf("original missing: %v", err)
	}

	// Pass 2 re-mints the SAME id with different text.
	revised := map[string]any{
		"instincts": []any{
			map[string]any{
				"id": "read-before-edit", "trigger": "when editing unfamiliar files",
				"confidence": 0.9, "domain": "workflow", "scope": "project",
				"action":   "Read the whole enclosing function, not just the edited line.",
				"evidence": []any{"observed 9 times"},
			},
		},
	}
	second := time.Date(2026, 6, 24, 9, 0, 0, 0, time.UTC)
	staged2, _, _ := stageInstincts(layout.StagingDir(ident.ID), ident, revised, second)
	out := screenAndPromote(context.Background(), layout, ident.ID, staged2, gate, nil, second)

	if out.Superseded != 1 || out.ArchiveDir == "" {
		t.Fatalf("Superseded=%d ArchiveDir=%q, want 1 and a dir (errs=%v)", out.Superseded, out.ArchiveDir, out.Errs)
	}
	// The prior version survives byte-for-byte in the archive …
	archived, err := os.ReadFile(filepath.Join(out.ArchiveDir, "read-before-edit.md"))
	if err != nil {
		t.Fatalf("archived prior version missing: %v", err)
	}
	if !bytes.Equal(archived, original) {
		t.Errorf("archived copy differs from the original it replaced")
	}
	// … and the personal corpus now holds the new text.
	live, _ := homunculus.ReadInstinctFile(filepath.Join(layout.InstinctsDir(ident.ID), "read-before-edit.md"))
	if live == nil || !strings.Contains(live.Body, "whole enclosing function") {
		t.Errorf("personal corpus does not hold the new version: %+v", live)
	}
	report, err := os.ReadFile(filepath.Join(out.ArchiveDir, "REPORT.md"))
	if err != nil {
		t.Fatalf("archive REPORT.md missing: %v", err)
	}
	for _, want := range []string{"superseded", "mv ", "read-before-edit.md"} {
		if !strings.Contains(string(report), want) {
			t.Errorf("archive REPORT.md missing %q\n---\n%s", want, report)
		}
	}
}

// TestPromote_IdenticalRemintIsNotArchived keeps the archive signal
// clean: re-minting identical text is reinforcement, not a supersede,
// and archiving it would bury real changes under duplicates.
func TestPromote_IdenticalRemintIsNotArchived(t *testing.T) {
	layout := homunculus.FromRoot(t.TempDir())
	ident := homunculus.ProjectIdentity{ID: "proj1", Name: "demo"}
	gate := instinctgate.New(instinctgate.Config{Enabled: true})
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)

	staged1, _, _ := stageInstincts(layout.StagingDir(ident.ID), ident, twoInstinctResult(), now)
	screenAndPromote(context.Background(), layout, ident.ID, staged1, gate, nil, now)
	// Same payload, same mint timestamp ⇒ byte-identical rendering.
	staged2, _, _ := stageInstincts(layout.StagingDir(ident.ID), ident, twoInstinctResult(), now)
	out := screenAndPromote(context.Background(), layout, ident.ID, staged2, gate, nil, now)

	if out.Superseded != 0 {
		t.Errorf("identical re-mint Superseded = %d, want 0", out.Superseded)
	}
	if _, err := os.Stat(layout.ArchiveDir(ident.ID)); !os.IsNotExist(err) {
		t.Errorf("archive dir created for an identical re-mint (err=%v)", err)
	}
}

// TestPromote_UnreadablePriorIsNotOverwritten is the regression net for
// the archive guarantee's blind spot: a prior instinct that EXISTS but
// cannot be read (permission, transient IO, a lock) was treated as
// "absent", so the promotion overwrote it with nothing archived and no
// error — the same silent destruction the archive exists to prevent.
//
// The promotion must be skipped instead: a mint left in staging can be
// promoted on the next run; a destroyed prior version cannot be recovered.
func TestPromote_UnreadablePriorIsNotOverwritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files, so the fault cannot be injected")
	}
	layout := homunculus.FromRoot(t.TempDir())
	ident := homunculus.ProjectIdentity{ID: "proj1", Name: "demo"}
	gate := instinctgate.New(instinctgate.Config{Enabled: true})

	first := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	staged1, _, _ := stageInstincts(layout.StagingDir(ident.ID), ident, twoInstinctResult(), first)
	screenAndPromote(context.Background(), layout, ident.ID, staged1, gate, nil, first)

	live := filepath.Join(layout.InstinctsDir(ident.ID), "read-before-edit.md")
	original, err := os.ReadFile(live)
	if err != nil {
		t.Fatalf("original missing: %v", err)
	}
	if err := os.Chmod(live, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(live, 0o644) })

	revised := map[string]any{
		"instincts": []any{
			map[string]any{
				"id": "read-before-edit", "trigger": "when editing unfamiliar files",
				"confidence": 0.9, "domain": "workflow", "scope": "project",
				"action":   "Read the whole enclosing function, not just the edited line.",
				"evidence": []any{"observed 9 times"},
			},
		},
	}
	second := time.Date(2026, 6, 24, 9, 0, 0, 0, time.UTC)
	staged2, _, _ := stageInstincts(layout.StagingDir(ident.ID), ident, revised, second)
	out := screenAndPromote(context.Background(), layout, ident.ID, staged2, gate, nil, second)

	if len(out.Errs) == 0 {
		t.Error("an unreadable prior version must be reported, not swallowed")
	}
	if out.Emitted != 0 {
		t.Errorf("Emitted = %d, want 0 — the promotion must be skipped, not overwrite the prior version", out.Emitted)
	}
	if err := os.Chmod(live, 0o644); err != nil {
		t.Fatalf("chmod back: %v", err)
	}
	after, err := os.ReadFile(live)
	if err != nil {
		t.Fatalf("prior version disappeared: %v", err)
	}
	if !bytes.Equal(after, original) {
		t.Error("the prior version was overwritten despite never being archived")
	}
	// The mint itself is not lost either — it is still staged …
	if _, err := os.Stat(staged2[0].path); err != nil {
		t.Fatalf("the staged mint should survive for the next run: %v", err)
	}
	// … and "survives" only counts if something ever picks it up again.
	// A file nobody re-reads is stranded, not preserved, so the next
	// pass must actually adopt it.
	adopted, aerrs := adoptStrandedStaged(layout.StagingDir(ident.ID), nil)
	if len(aerrs) != 0 {
		t.Errorf("unexpected adoption errors: %v", aerrs)
	}
	var found bool
	for _, a := range adopted {
		if a.in.ID == "read-before-edit" {
			found = true
			if a.action == "" {
				t.Error("the adopted candidate has no action line, so the gate would screen an empty surface")
			}
		}
	}
	if !found {
		t.Errorf("the stranded mint was not adopted by the next pass: %+v", adopted)
	}

	// Once the prior version is readable again, the adopted mint promotes.
	if err := os.Chmod(live, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	third := time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC)
	out3 := screenAndPromote(context.Background(), layout, ident.ID, adopted, gate, nil, third)
	if out3.Emitted != 1 {
		t.Errorf("Emitted = %d, want 1 — the recovered mint should land (errs=%v)", out3.Emitted, out3.Errs)
	}
	recovered, _ := homunculus.ReadInstinctFile(live)
	if recovered == nil || !strings.Contains(recovered.Body, "whole enclosing function") {
		t.Errorf("the personal corpus does not hold the recovered mint: %+v", recovered)
	}
}

// TestAdoptStrandedStagedSkipsFreshlyMintedIDs pins the de-dup: an id the
// current pass just re-minted must not also be adopted from staging, or
// the same id would be screened twice in one batch.
func TestAdoptStrandedStagedSkipsFreshlyMintedIDs(t *testing.T) {
	layout := homunculus.FromRoot(t.TempDir())
	ident := homunculus.ProjectIdentity{ID: "proj1", Name: "demo"}
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	staged := stagedFromResult(t, layout, ident, now)

	adopted, errs := adoptStrandedStaged(layout.StagingDir(ident.ID), staged)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(adopted) != 0 {
		t.Errorf("ids minted this pass must not be adopted again, got %d", len(adopted))
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
	out := screenAndPromote(context.Background(), layout, ident.ID, staged, gate, nil, now)

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
