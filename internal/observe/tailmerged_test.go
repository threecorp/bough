package observe

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTailNMerged is the regression test for the v0.9.5 fix: observer
// run-once must read BOTH the hook inbox and the homunculus archive, or
// hook-captured observations never reach the miner.
func TestTailNMerged(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "archive.jsonl") // ecc import writes here
	inbox := filepath.Join(dir, "inbox.jsonl")     // hook handle writes here
	if err := os.WriteFile(archive, []byte(`{"event":"a1"}`+"\n"+`{"event":"a2"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inbox, []byte(`{"event":"i1"}`+"\n"+`{"event":"i2"}`+"\n"+`{"event":"i3"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// both sources merge; these fixtures carry no TS, so the stable
	// recency sort preserves append order (archive first, then inbox)
	got, err := TailNMerged(10, archive, inbox)
	if err != nil {
		t.Fatalf("TailNMerged: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("merged count = %d, want 5 (2 archive + 3 inbox)", len(got))
	}
	if got[len(got)-1].Event != "i3" {
		t.Errorf("last = %q, want i3 (equal TS → stable append order)", got[len(got)-1].Event)
	}

	// a missing source is skipped, not an error (either producer may not
	// have run yet) — this is exactly the hook-only / import-only case.
	only, err := TailNMerged(10, filepath.Join(dir, "nope.jsonl"), inbox)
	if err != nil {
		t.Fatalf("missing source must be skipped: %v", err)
	}
	if len(only) != 3 {
		t.Errorf("with one missing source, count = %d, want 3 (inbox only)", len(only))
	}

	// the tail cap still applies across the merge
	capped, err := TailNMerged(2, archive, inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped) != 2 || capped[1].Event != "i3" {
		t.Errorf("tail cap = %d (%+v), want 2 ending i3", len(capped), capped)
	}
}

// TestTailNMerged_SortsByRecency is the regression test for the starvation
// bug: the tail must be the n most-RECENT observations by TS, not the tail
// of whichever file was listed last. A large STALE file (the legacy inbox,
// listed last) must not crowd out the live archive's recent records
// (listed first). Before the TS sort this returned the stale inbox tail.
func TestTailNMerged_SortsByRecency(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "archive.jsonl") // live; recent
	inbox := filepath.Join(dir, "inbox.jsonl")     // stale legacy; old
	if err := os.WriteFile(archive, []byte(
		`{"ts":"2026-06-30T10:00:00Z","event":"recent1"}`+"\n"+
			`{"ts":"2026-06-30T11:00:00Z","event":"recent2"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inbox, []byte(
		`{"ts":"2020-01-01T00:00:00Z","event":"old1"}`+"\n"+
			`{"ts":"2020-01-01T00:01:00Z","event":"old2"}`+"\n"+
			`{"ts":"2020-01-01T00:02:00Z","event":"old3"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// archive listed FIRST, stale inbox LAST; a window of 2 must return the
	// two most-recent (both from the archive), NOT the inbox tail.
	got, err := TailNMerged(2, archive, inbox)
	if err != nil {
		t.Fatalf("TailNMerged: %v", err)
	}
	if len(got) != 2 || got[0].Event != "recent1" || got[1].Event != "recent2" {
		t.Errorf("tail-by-recency = %+v, want [recent1 recent2] (live archive not starved by stale inbox)", got)
	}
}
