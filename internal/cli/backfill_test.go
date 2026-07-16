package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ikeikeikeike/bough/internal/config"
)

// TestRunBackfill_RelinksExistingWorktrees is the regression for #61: before
// this, linkWorktreeArtifacts was wired ONLY into `bough create`, so a
// worktree that predates the project-scope evolved-artifact move — or was
// itself registered by an earlier `bough backfill` run before this fix —
// never got its .claude/{skills,agents,commands} symlinks and silently
// loaded zero evolved artifacts. backfill must relink EVERY discovered
// worktree dir, not just newly-registered ones.
func TestRunBackfill_RelinksExistingWorktrees(t *testing.T) {
	mono := t.TempDir()
	wtDir := filepath.Join(mono, "worktrees", "F-existing")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-populate a project-scoped skill so the relink has something real
	// to point at (mirrors what `bough evolve` would have already deployed).
	if err := os.MkdirAll(filepath.Join(mono, ".claude", "skills", "s1"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Registry: config.RegistryConfig{Path: filepath.Join(mono, ".bough-ports.json")}}
	var stderr bytes.Buffer
	if err := runBackfill(&stderr, cfg, mono, mono); err != nil {
		t.Fatalf("runBackfill: %v", err)
	}

	link := filepath.Join(wtDir, ".claude", "skills")
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("backfill did not create the worktree skills symlink: %v", err)
	}
	if want := filepath.Join(mono, ".claude", "skills"); got != want {
		t.Errorf("link = %q, want %q", got, want)
	}

	// Re-running backfill against the now-ALREADY-REGISTERED worktree must
	// still relink — the relink must not be gated on "newly added to the
	// registry" (that was the exact gap #61 reported: pre-existing /
	// already-backfilled worktrees never got relinked).
	if err := runBackfill(&stderr, cfg, mono, mono); err != nil {
		t.Fatalf("second runBackfill: %v", err)
	}
	if got2, err := os.Readlink(link); err != nil || got2 != got {
		t.Errorf("relink on second run: got=%q err=%v, want unchanged %q", got2, err, got)
	}
}

// TestRunBackfill_NoWorktreesDirIsANoop preserves the pre-existing
// behaviour: an absent worktrees/ dir is not an error.
func TestRunBackfill_NoWorktreesDirIsANoop(t *testing.T) {
	mono := t.TempDir()
	cfg := &config.Config{Registry: config.RegistryConfig{Path: filepath.Join(mono, ".bough-ports.json")}}
	var stderr bytes.Buffer
	if err := runBackfill(&stderr, cfg, mono, mono); err != nil {
		t.Fatalf("runBackfill on a monorepo with no worktrees dir should be a no-op, got: %v", err)
	}
}

// TestRunBackfill_RelinksClaudeMd guards against the CLAUDE.md analogue of
// #61: #106 wired linkWorktreeClaudeMd into `bough create` only, never into
// `bough backfill`, so a worktree that predates the CLAUDE.md-symlink feature
// (or a hand-created dir backfill merely registers) never got the link and
// silently kept loading with no root guidance. backfill must relink CLAUDE.md
// for EVERY discovered worktree dir, exactly like it already does for
// .claude/{skills,agents,commands}.
func TestRunBackfill_RelinksClaudeMd(t *testing.T) {
	mono := t.TempDir()
	wtDir := filepath.Join(mono, "worktrees", "F-existing")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootClaudeMd := filepath.Join(mono, "CLAUDE.md")
	if err := os.WriteFile(rootClaudeMd, []byte("# root guidance\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Registry: config.RegistryConfig{Path: filepath.Join(mono, ".bough-ports.json")}}
	var stderr bytes.Buffer
	if err := runBackfill(&stderr, cfg, mono, mono); err != nil {
		t.Fatalf("runBackfill: %v", err)
	}

	link := filepath.Join(wtDir, "CLAUDE.md")
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("backfill did not create the worktree CLAUDE.md symlink: %v", err)
	}
	if got != rootClaudeMd {
		t.Errorf("link = %q, want %q", got, rootClaudeMd)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("link target must be absolute: %q", got)
	}

	// Re-running backfill against the now-ALREADY-REGISTERED worktree must
	// still relink, same as the .claude artifacts case.
	if err := runBackfill(&stderr, cfg, mono, mono); err != nil {
		t.Fatalf("second runBackfill: %v", err)
	}
	if got2, err := os.Readlink(link); err != nil || got2 != got {
		t.Errorf("relink on second run: got=%q err=%v, want unchanged %q", got2, err, got)
	}
}

// TestRunBackfill_ClaudeMdRealFileGuardAndMissingRoot exercises the two edge
// cases linkWorktreeClaudeMd itself is expected to handle: a hand-authored
// real CLAUDE.md already in a worktree must survive backfill untouched, and a
// monorepo root with no CLAUDE.md at all must not error or create a dangling
// symlink anywhere.
func TestRunBackfill_ClaudeMdRealFileGuardAndMissingRoot(t *testing.T) {
	mono := t.TempDir()
	wtDir := filepath.Join(mono, "worktrees", "F-existing")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(wtDir, "CLAUDE.md")
	if err := os.WriteFile(realFile, []byte("operator's own\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Deliberately no monorepo-root CLAUDE.md.

	cfg := &config.Config{Registry: config.RegistryConfig{Path: filepath.Join(mono, ".bough-ports.json")}}
	var stderr bytes.Buffer
	if err := runBackfill(&stderr, cfg, mono, mono); err != nil {
		t.Fatalf("runBackfill with no root CLAUDE.md should not error, got: %v", err)
	}

	fi, err := os.Lstat(realFile)
	if err != nil {
		t.Fatalf("real worktree CLAUDE.md vanished: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("a real worktree CLAUDE.md was clobbered into a symlink")
	}
	if b, _ := os.ReadFile(realFile); string(b) != "operator's own\n" {
		t.Errorf("real worktree CLAUDE.md content was modified")
	}
}
