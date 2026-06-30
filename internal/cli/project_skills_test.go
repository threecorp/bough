package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureSymlink covers the shared idempotent-symlink helper used by the
// project-scoped skill deploy + the worktree skills link.
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

// TestDeployProjectSkills verifies evolved skills are symlinked into the
// monorepo's project-scoped .claude/skills (a dir without SKILL.md is skipped).
func TestDeployProjectSkills(t *testing.T) {
	tmp := t.TempDir()
	evolved := filepath.Join(tmp, "homunculus", "evolved", "skills")
	_ = os.MkdirAll(filepath.Join(evolved, "s1"), 0o755)
	_ = os.WriteFile(filepath.Join(evolved, "s1", "SKILL.md"), []byte("# s1\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(evolved, "notaskill"), 0o755) // no SKILL.md → skip

	root := filepath.Join(tmp, "mono")
	deployProjectSkills(io.Discard, io.Discard, evolved, root)

	got, err := os.Readlink(filepath.Join(root, ".claude", "skills", "s1"))
	if err != nil || got != filepath.Join(evolved, "s1") {
		t.Errorf("s1 project link not created: got=%q err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".claude", "skills", "notaskill")); !os.IsNotExist(err) {
		t.Errorf("a dir without SKILL.md must not be linked")
	}
}

// TestLinkWorktreeSkills verifies the worktree gets an absolute symlink to the
// monorepo's project-scoped skills, and a pre-existing real dir is not clobbered.
func TestLinkWorktreeSkills(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")
	_ = os.MkdirAll(wt, 0o755)

	linkWorktreeSkills(io.Discard, root, wt)

	link := filepath.Join(wt, ".claude", "skills")
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("worktree skills symlink not created: %v", err)
	}
	want := filepath.Join(root, ".claude", "skills")
	if got != want {
		t.Errorf("link = %q, want %q", got, want)
	}
	if !isDir(want) {
		t.Errorf("monorepo .claude/skills was not created at %q", want)
	}

	// real-dir guard: a pre-existing real <wt>/.claude/skills must survive
	wt2 := filepath.Join(t.TempDir(), "wt2")
	realSkills := filepath.Join(wt2, ".claude", "skills")
	_ = os.MkdirAll(realSkills, 0o755)
	linkWorktreeSkills(io.Discard, root, wt2)
	if fi, _ := os.Lstat(realSkills); fi != nil && fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("a real worktree .claude/skills was clobbered into a symlink")
	}
}
