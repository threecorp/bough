package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureSymlink covers the idempotent-symlink helper that links a
// worktree's CLAUDE.md to the monorepo root's.
func TestEnsureSymlink(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "sub", "link") // parent created by ensureSymlink

	if err := ensureSymlink(target, link); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, _ := os.Readlink(link); got != target {
		t.Errorf("link target = %q, want %q", got, target)
	}
	// idempotent — re-run on an already-correct link is a no-op, no error
	if err := ensureSymlink(target, link); err != nil {
		t.Fatalf("idempotent re-run: %v", err)
	}
	// repoint a stale symlink
	target2 := filepath.Join(tmp, "target2")
	_ = os.MkdirAll(target2, 0o755)
	if err := ensureSymlink(target2, link); err != nil {
		t.Fatalf("repoint: %v", err)
	}
	if got, _ := os.Readlink(link); got != target2 {
		t.Errorf("repointed = %q, want %q", got, target2)
	}
	// refuse to clobber a real (non-symlink) dir, citing the reason
	realDir := filepath.Join(tmp, "real")
	_ = os.MkdirAll(realDir, 0o755)
	if err := ensureSymlink(target, realDir); err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Errorf("ensureSymlink must refuse to clobber a real dir: got %v", err)
	}
	// refuse to clobber a real (non-symlink) FILE a maintainer hand-authored,
	// and leave its contents intact
	realFile := filepath.Join(tmp, "hand-authored")
	_ = os.WriteFile(realFile, []byte("operator's skill"), 0o644)
	if err := ensureSymlink(target, realFile); err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Errorf("ensureSymlink must refuse to clobber a real file: got %v", err)
	}
	if b, _ := os.ReadFile(realFile); string(b) != "operator's skill" {
		t.Errorf("hand-authored file content was modified")
	}
	// a RELATIVE target is stored as an ABSOLUTE link, so the link resolves the
	// same regardless of the reader's CWD (ensureSymlink's documented contract)
	relLink := filepath.Join(tmp, "rel-link")
	if err := ensureSymlink("rel/ative/target", relLink); err != nil {
		t.Fatalf("relative target: %v", err)
	}
	if got, _ := os.Readlink(relLink); !filepath.IsAbs(got) {
		t.Errorf("relative target was not made absolute: %q", got)
	}
}

// TestLinkWorktreeClaudeMd verifies the worktree gets an absolute symlink to the
// monorepo root's CLAUDE.md, that a missing root CLAUDE.md is a no-op, and that a
// pre-existing real CLAUDE.md in the worktree is left intact.
func TestLinkWorktreeClaudeMd(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# root guidance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	_ = os.MkdirAll(wt, 0o755)

	linkWorktreeClaudeMd(io.Discard, root, wt)

	link := filepath.Join(wt, "CLAUDE.md")
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("worktree CLAUDE.md symlink not created: %v", err)
	}
	want := filepath.Join(root, "CLAUDE.md")
	if got != want {
		t.Errorf("link = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("link target must be absolute (resolves regardless of CWD): %q", got)
	}

	// no root CLAUDE.md → no-op: no symlink created, no error
	emptyRoot := t.TempDir()
	wt2 := filepath.Join(t.TempDir(), "wt2")
	_ = os.MkdirAll(wt2, 0o755)
	linkWorktreeClaudeMd(io.Discard, emptyRoot, wt2)
	if _, err := os.Lstat(filepath.Join(wt2, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("a missing root CLAUDE.md must not create a worktree symlink")
	}

	// real-file guard: a hand-authored real <wt>/CLAUDE.md must survive intact
	wt3 := filepath.Join(t.TempDir(), "wt3")
	_ = os.MkdirAll(wt3, 0o755)
	realFile := filepath.Join(wt3, "CLAUDE.md")
	_ = os.WriteFile(realFile, []byte("operator's own\n"), 0o644)
	linkWorktreeClaudeMd(io.Discard, root, wt3)
	if fi, _ := os.Lstat(realFile); fi != nil && fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("a real worktree CLAUDE.md was clobbered into a symlink")
	}
	if b, _ := os.ReadFile(realFile); string(b) != "operator's own\n" {
		t.Errorf("real worktree CLAUDE.md content was modified")
	}
}
