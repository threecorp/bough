package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureSymlink covers the shared idempotent-symlink helper used by the
// project-scoped artifact deploy + the worktree artifact link.
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

// TestDeployProjectFiles verifies flat-file evolved artifacts (agents,
// commands) are symlinked into the monorepo's project-scoped .claude/<kind>
// (a non-.md entry, and a subdirectory, are both skipped).
func TestDeployProjectFiles(t *testing.T) {
	tmp := t.TempDir()
	evolved := filepath.Join(tmp, "homunculus", "evolved", "agents")
	_ = os.MkdirAll(evolved, 0o755)
	_ = os.WriteFile(filepath.Join(evolved, "a1.md"), []byte("# a1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(evolved, "notes.txt"), []byte("x"), 0o644) // not .md → skip
	_ = os.MkdirAll(filepath.Join(evolved, "a1.md.bak"), 0o755)               // a dir → skip regardless of name

	projectDir := filepath.Join(tmp, "mono", ".claude", "agents")
	deployProjectFiles(io.Discard, io.Discard, "agent", evolved, projectDir)

	got, err := os.Readlink(filepath.Join(projectDir, "a1.md"))
	if err != nil || got != filepath.Join(evolved, "a1.md") {
		t.Errorf("a1.md project link not created: got=%q err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(projectDir, "notes.txt")); !os.IsNotExist(err) {
		t.Errorf("a non-.md file must not be linked")
	}
	if _, err := os.Lstat(filepath.Join(projectDir, "a1.md.bak")); !os.IsNotExist(err) {
		t.Errorf("a directory must not be linked even with a .md-like name")
	}
}

// TestDeployProjectArtifacts is the regression for #62: before this, only
// skills were deployed to project scope — every evolved agent/command was
// written and then orphaned. Rules later repeated the same mistake, and a
// rule that is not under .claude/rules never fires at all — the path-scoped
// delivery it exists for is the only thing it does. It verifies all four
// evolved kinds land under <monorepoRoot>/.claude/{skills,agents,commands,rules}.
func TestDeployProjectArtifacts(t *testing.T) {
	tmp := t.TempDir()
	skillsDir := filepath.Join(tmp, "evolved", "skills")
	agentsDir := filepath.Join(tmp, "evolved", "agents")
	commandsDir := filepath.Join(tmp, "evolved", "commands")
	rulesDir := filepath.Join(tmp, "evolved", "rules")
	_ = os.MkdirAll(filepath.Join(skillsDir, "s1"), 0o755)
	_ = os.WriteFile(filepath.Join(skillsDir, "s1", "SKILL.md"), []byte("# s1\n"), 0o644)
	_ = os.MkdirAll(agentsDir, 0o755)
	_ = os.WriteFile(filepath.Join(agentsDir, "a1.md"), []byte("# a1\n"), 0o644)
	_ = os.MkdirAll(commandsDir, 0o755)
	_ = os.WriteFile(filepath.Join(commandsDir, "c1.md"), []byte("# c1\n"), 0o644)
	_ = os.MkdirAll(rulesDir, 0o755)
	_ = os.WriteFile(filepath.Join(rulesDir, "r1.md"), []byte("# r1\n"), 0o644)

	root := filepath.Join(tmp, "mono")
	deployProjectArtifacts(io.Discard, io.Discard, skillsDir, agentsDir, commandsDir, rulesDir, root)

	for _, want := range []struct{ kind, name, wantTarget string }{
		{"skills", "s1", filepath.Join(skillsDir, "s1")},
		{"agents", "a1.md", filepath.Join(agentsDir, "a1.md")},
		{"commands", "c1.md", filepath.Join(commandsDir, "c1.md")},
		{"rules", "r1.md", filepath.Join(rulesDir, "r1.md")},
	} {
		got, err := os.Readlink(filepath.Join(root, ".claude", want.kind, want.name))
		if err != nil || got != want.wantTarget {
			t.Errorf("%s/%s: link = %q, err = %v, want target %q", want.kind, want.name, got, err, want.wantTarget)
		}
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

// TestLinkWorktreeArtifacts verifies the worktree gets absolute symlinks to
// the monorepo's project-scoped skills/agents/commands (#62 extended this
// from skills-only), and a pre-existing real dir is not clobbered.
func TestLinkWorktreeArtifacts(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")
	_ = os.MkdirAll(wt, 0o755)

	linkWorktreeArtifacts(io.Discard, root, wt)

	for _, kind := range []string{"skills", "agents", "commands"} {
		link := filepath.Join(wt, ".claude", kind)
		got, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("worktree .claude/%s symlink not created: %v", kind, err)
		}
		want := filepath.Join(root, ".claude", kind)
		if got != want {
			t.Errorf(".claude/%s: link = %q, want %q", kind, got, want)
		}
		if !isDir(want) {
			t.Errorf("monorepo .claude/%s was not created at %q", kind, want)
		}
	}

	// real-dir guard: a pre-existing real <wt>/.claude/skills must survive
	wt2 := filepath.Join(t.TempDir(), "wt2")
	realSkills := filepath.Join(wt2, ".claude", "skills")
	_ = os.MkdirAll(realSkills, 0o755)
	linkWorktreeArtifacts(io.Discard, root, wt2)
	if fi, _ := os.Lstat(realSkills); fi != nil && fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("a real worktree .claude/skills was clobbered into a symlink")
	}
}
