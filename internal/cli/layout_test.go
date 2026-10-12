package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/config"
)

// TestWorktreeSourceRepo pins Fix for the create/remove drift: the
// source repo of a linked worktree must be recoverable from the
// worktree's own gitlink, so `bough remove` targets the exact repo
// `bough create` registered the worktree against regardless of what
// resolveRepoSrc would now guess from live disk state.
func TestWorktreeSourceRepo(t *testing.T) {
	src := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", src}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init")

	wt := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.Command("git", "-C", src, "worktree", "add", wt, "-b", "feat").CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}

	got, ok := worktreeSourceRepo(wt)
	if !ok {
		t.Fatal("worktreeSourceRepo returned ok=false for a real linked worktree")
	}
	// macOS t.TempDir() lives behind /private symlinks; compare resolved.
	want, _ := filepath.EvalSymlinks(src)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != want {
		t.Errorf("worktreeSourceRepo = %q (resolved %q), want the source repo %q", got, gotResolved, want)
	}

	if _, ok := worktreeSourceRepo(t.TempDir()); ok {
		t.Error("worktreeSourceRepo must return ok=false for a non-git dir (caller falls back to resolveRepoSrc)")
	}
}

// TestWarnIfRootNotGit pins the 竹 heads-up: a non-git monorepo root
// gets a warning that `--worktree X --resume Y` won't find sessions
// started there, plus a .gitignore suggestion that reflects the layout
// THIS monorepo actually uses; a git-initialised root stays silent.
func TestWarnIfRootNotGit(t *testing.T) {
	t.Run("fresh non-git root suggests the v0.11 layout", func(t *testing.T) {
		root := t.TempDir()
		var buf bytes.Buffer
		inside, determined := insideGitWorkTree(root)
		warnIfRootNotGit(&buf, &config.Config{}, root, inside, determined)
		out := buf.String()
		for _, want := range []string{"not a git repository", "--resume", ".bough/", "worktrees/"} {
			if !strings.Contains(out, want) {
				t.Errorf("warning missing %q; got:\n%s", want, out)
			}
		}
	})

	t.Run("legacy .worktrees/ layout is suggested verbatim, not the new name", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".worktrees"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// A pre-v0.11 root-level checkout that .bough/ would NOT cover.
		mkGitRepo(t, filepath.Join(root, "demo-api"))
		var buf bytes.Buffer
		inside, determined := insideGitWorkTree(root)
		warnIfRootNotGit(&buf, &config.Config{Repositories: []config.Repository{{Name: "demo-api"}}}, root, inside, determined)
		got := gitignoreSuggestions(&config.Config{Repositories: []config.Repository{{Name: "demo-api"}}}, root)
		want := []string{".bough/", ".worktrees/", "demo-api/"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("gitignoreSuggestions = %v, want %v (legacy .worktrees/ + root-level checkout must be covered)", got, want)
		}
		if strings.Contains(buf.String(), " worktrees/\n") {
			t.Errorf("must not suggest the non-hidden worktrees/ when the legacy .worktrees/ is in use:\n%s", buf.String())
		}
	})

	// The registry and its .lock sit outside .bough/ on the pre-v0.11
	// layout (and wherever registry.path points), so .bough/ alone left
	// both untracked-but-unignored.
	t.Run("a registry outside .bough/ is suggested with its lock", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".bough-ports.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{Registry: config.RegistryConfig{Path: ".bough/ports.json"}}
		got := strings.Join(gitignoreSuggestions(cfg, root), ",")
		if want := ".bough-ports.json,.bough-ports.json.lock,.bough/,worktrees/"; got != want {
			t.Errorf("legacy registry: gitignoreSuggestions = %s, want %s", got, want)
		}

		fresh := t.TempDir()
		got = strings.Join(gitignoreSuggestions(cfg, fresh), ",")
		if want := ".bough/,worktrees/"; got != want {
			t.Errorf("registry under .bough/: gitignoreSuggestions = %s, want %s", got, want)
		}
	})

	t.Run("git root stays silent", func(t *testing.T) {
		root := t.TempDir()
		if out, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		var buf bytes.Buffer
		inside, determined := insideGitWorkTree(root)
		warnIfRootNotGit(&buf, &config.Config{}, root, inside, determined)
		if buf.Len() != 0 {
			t.Errorf("git root must stay silent; got:\n%s", buf.String())
		}
	})
}

// mkGitRepo makes dir look like an acquired repo to isGitRepo (a `.git`
// entry present), the same shape materializeRepositories checks before
// deciding whether to clone.
func mkGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkGitRepo %s: %v", dir, err)
	}
}

// TestResolveRepoSrc pins the backward-compat resolution order for a
// repo's source checkout: the new `.bough/repos/<name>` location wins
// when present, an existing root-level `<root>/<name>` checkout is still
// honored (so upgrading bough never orphans an already-acquired repo),
// and a fresh acquisition (neither present) lands in the new location.
func TestResolveRepoSrc(t *testing.T) {
	t.Run("new .bough/repos location wins when present", func(t *testing.T) {
		root := t.TempDir()
		newLoc := filepath.Join(root, ".bough", "repos", "demo-api")
		mkGitRepo(t, newLoc)
		mkGitRepo(t, filepath.Join(root, "demo-api")) // old also present
		if got := resolveRepoSrc(root, "demo-api"); got != newLoc {
			t.Errorf("resolveRepoSrc = %q, want the new .bough/repos location %q", got, newLoc)
		}
	})

	t.Run("falls back to existing root-level checkout", func(t *testing.T) {
		root := t.TempDir()
		oldLoc := filepath.Join(root, "demo-api")
		mkGitRepo(t, oldLoc)
		if got := resolveRepoSrc(root, "demo-api"); got != oldLoc {
			t.Errorf("resolveRepoSrc = %q, want the existing root-level checkout %q (must not orphan it)", got, oldLoc)
		}
	})

	t.Run("fresh acquisition targets the new location", func(t *testing.T) {
		root := t.TempDir()
		want := filepath.Join(root, ".bough", "repos", "demo-api")
		if got := resolveRepoSrc(root, "demo-api"); got != want {
			t.Errorf("resolveRepoSrc = %q, want a fresh clone to target %q", got, want)
		}
	})
}

// TestResolveRegistryPath pins the registry-file resolution order: the
// v0.11 `.bough/ports.json` wins when present (so a migrated monorepo
// stops reading the flat file), the pre-v0.11 `.bough-ports.json` is
// still honored, and otherwise the operator's configured path is used.
func TestResolveRegistryPath(t *testing.T) {
	write := func(t *testing.T, p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	t.Run("new .bough/ports.json wins over the legacy flat file", func(t *testing.T) {
		root := t.TempDir()
		newp := filepath.Join(root, ".bough", "ports.json")
		write(t, newp)
		write(t, filepath.Join(root, ".bough-ports.json")) // legacy also present
		if got := resolveRegistryPath(root, ".bough/ports.json"); got != newp {
			t.Errorf("resolveRegistryPath = %q, want the new %q", got, newp)
		}
	})

	t.Run("legacy .bough-ports.json still found", func(t *testing.T) {
		root := t.TempDir()
		legacy := filepath.Join(root, ".bough-ports.json")
		write(t, legacy)
		if got := resolveRegistryPath(root, ".bough/ports.json"); got != legacy {
			t.Errorf("resolveRegistryPath = %q, want the legacy %q (must stay readable)", got, legacy)
		}
	})

	t.Run("neither present falls back to the configured path", func(t *testing.T) {
		root := t.TempDir()
		want := filepath.Join(root, ".bough", "ports.json")
		if got := resolveRegistryPath(root, ".bough/ports.json"); got != want {
			t.Errorf("resolveRegistryPath = %q, want the configured %q", got, want)
		}
	})
}

// TestWorktreesDir pins the worktrees-directory choice: a monorepo that
// already has the legacy hidden `.worktrees/` keeps using it (existing
// worktrees stay findable), while a fresh monorepo uses the non-hidden
// `worktrees/`.
func TestWorktreesDir(t *testing.T) {
	t.Run("legacy .worktrees kept when present", func(t *testing.T) {
		root := t.TempDir()
		legacy := filepath.Join(root, ".worktrees")
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if got := worktreesDir(root); got != legacy {
			t.Errorf("worktreesDir = %q, want the legacy %q (existing worktrees must stay findable)", got, legacy)
		}
	})

	t.Run("fresh monorepo uses non-hidden worktrees", func(t *testing.T) {
		root := t.TempDir()
		want := filepath.Join(root, "worktrees")
		if got := worktreesDir(root); got != want {
			t.Errorf("worktreesDir = %q, want the non-hidden %q", got, want)
		}
	})
}

// TestWorktreePathFor pins the guard every name-based entry point shares.
// A worktree name is one directory name: "..", "../x" reached the monorepo
// root, "feat" on a nested "feat/x" made remove delete feat/x, and "b/",
// "./b", "/b" or "a/../b" reached worktrees/b under a name the registry
// and lock did not know.
func TestWorktreePathFor(t *testing.T) {
	root := t.TempDir()
	dir := worktreesDir(root)
	for _, name := range []string{"F-Auth", "fix.1_x"} {
		got, err := worktreePathFor(root, name)
		if err != nil || got != filepath.Join(dir, name) {
			t.Errorf("worktreePathFor(%q) = (%q, %v), want %q", name, got, err, filepath.Join(dir, name))
		}
	}
	for _, name := range []string{"", ".", "..", "../x", "a/../..", "../worktrees-x", "feat/x", "b/", "./b", "/b", "a/../b", `a\b`} {
		if got, err := worktreePathFor(root, name); err == nil {
			t.Errorf("worktreePathFor(%q) = %q, want an error", name, got)
		}
	}
}

// TestWorktreePathFor_RefusesASymlinkedWorktree: remove runs pre_remove,
// Cleanup and the git fallback rm -rf inside the worktree's repos, so a
// worktrees/<name> symlink pointed all of them at its target.
func TestWorktreePathFor_RefusesASymlinkedWorktree(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(worktreesDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(worktreesDir(root), "F-Link")); err != nil {
		t.Fatal(err)
	}
	if _, err := worktreePathFor(root, "F-Link"); err == nil {
		t.Error("worktreePathFor accepted a symlinked worktree dir")
	}
}

// TestWorktreePathFor_RefusesACaseAlias: on a case-insensitive filesystem
// "F-AUTH" opens worktrees/F-Auth, but the registry and lock key on the
// name, so remove skipped F-Auth's engines and deleted its datadir.
func TestWorktreePathFor_RefusesACaseAlias(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(worktreesDir(root), "F-Auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktreesDir(root), "F-AUTH")); err != nil {
		t.Skip("case-sensitive filesystem: F-AUTH is a different directory here")
	}
	if _, err := worktreePathFor(root, "F-AUTH"); err == nil {
		t.Error("worktreePathFor accepted F-AUTH for the existing F-Auth")
	}
	if _, err := worktreePathFor(root, "F-Auth"); err != nil {
		t.Errorf("worktreePathFor refused the exact name: %v", err)
	}
}

func TestResolveRemoveTarget_RefusesTargetOutsideWorktrees(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if _, _, path, err := resolveRemoveTarget("..", ""); err == nil {
		t.Errorf("name %q resolved to %q, want an error", "..", path)
	}
	for _, p := range []string{root, filepath.Join(root, "other", "F-Auth")} {
		if _, _, _, err := resolveRemoveTarget("", p); err == nil {
			t.Errorf("path %q accepted, want an error (not directly inside the worktrees dir)", p)
		}
	}
	want := filepath.Join(worktreesDir(root), "F-Auth")
	if _, name, path, err := resolveRemoveTarget("", want); err != nil || name != "F-Auth" || path != want {
		t.Errorf("worktree path = (%q, %q, %v), want (F-Auth, %q, nil)", name, path, err, want)
	}
	// A relative --path used to reach Down as a relative WorktreeRoot, so
	// the owner check refused this worktree's own container.
	if _, _, path, err := resolveRemoveTarget("", filepath.Join(worktreesName, "F-Auth")); err != nil || !filepath.IsAbs(path) {
		t.Errorf("relative worktree path resolved to (%q, %v), want an absolute path", path, err)
	}
	if err := os.MkdirAll(worktreesDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(worktreesDir(root), "F-Link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := resolveRemoveTarget("", link); err == nil {
		t.Error("a symlinked worktree path was accepted")
	}
}

// TestRemoveCmd_DotDotLeavesRootIntact drives the CLI the way an operator
// would: before the guard, `bough remove --name ..` ran pre_remove in the
// root and then removed the whole monorepo root.
func TestRemoveCmd_DotDotLeavesRootIntact(t *testing.T) {
	root := t.TempDir()
	yaml := "schema_version: 2\nmonorepo_root: .\nrepositories:\n  - name: demo\n    branch_strategy: main\nregistry:\n  path: .bough/ports.json\n"
	if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	cmd := newRemoveCmd()
	cmd.SetArgs([]string{"--name", ".."})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Error("remove --name .. succeeded, want an error")
	}
	if _, err := os.Stat(filepath.Join(root, ".bough.yaml")); err != nil {
		t.Errorf("monorepo root was touched: %v", err)
	}
}

func TestCreateCmd_RefusesNameOutsideWorktrees(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	yaml := "schema_version: 2\nmonorepo_root: .\nrepositories:\n  - name: demo\n    branch_strategy: main\nregistry:\n  path: .bough/ports.json\n"
	if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newCreateCmd()
	cmd.SetArgs([]string{"--name", "..", "--cwd", root, "--no-fetch"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Error("create --name .. succeeded, want an error")
	}
	if _, err := os.Stat(filepath.Join(root, ".bough", "ports.json")); !os.IsNotExist(err) {
		t.Errorf("create reserved ports for an invalid name: %v", err)
	}
}

// sameNameFixture writes a config-only monorepo root and returns it with
// the worktree path bough uses for name.
func sameNameFixture(t *testing.T, name string) (root, wt string) {
	t.Helper()
	root = t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	yaml := "schema_version: 2\nmonorepo_root: .\nrepositories:\n  - name: demo\n    branch_strategy: main\nregistry:\n  path: .bough/ports.json\n"
	if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(worktreesDir(root), name)
}

// waitBlocked runs fn and asserts it does not return while the worktree
// lock is held, then releases the lock and waits for fn.
func waitBlocked(t *testing.T, release func(), fn func() error, stillThere func() bool) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case <-done:
		release()
		t.Fatal("ran while another create/remove of the same name held the worktree lock")
	case <-time.After(500 * time.Millisecond):
	}
	if !stillThere() {
		t.Error("state changed while the worktree lock was held")
	}
	release()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("never finished after the worktree lock was released")
	}
}

// TestRemove_WaitsForSameNameLifecycle: a remove that ran while a create of
// the same name was starting engines deleted the datadir under them.
func TestRemove_WaitsForSameNameLifecycle(t *testing.T) {
	root, wt := sameNameFixture(t, "F-X")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	release, err := lockWorktree(root, "F-X")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	cmd := newRemoveCmd()
	cmd.SetArgs([]string{"--name", "F-X"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	waitBlocked(t, release, cmd.Execute, func() bool { _, err := os.Stat(wt); return err == nil })
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("remove did not delete %s once it got the lock: %v", wt, err)
	}
}

func TestCreate_WaitsForSameNameLifecycle(t *testing.T) {
	root, wt := sameNameFixture(t, "F-X")
	release, err := lockWorktree(root, "F-X")
	if err != nil {
		t.Fatal(err)
	}
	cmd := newCreateCmd()
	cmd.SetArgs([]string{"--name", "F-X", "--cwd", root, "--no-fetch"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	waitBlocked(t, release, cmd.Execute, func() bool { _, err := os.Stat(wt); return os.IsNotExist(err) })
}

// TestLockWorktree_LongName: escaping turns each UTF-8 byte into %XX, so a
// long non-ASCII name exceeded the 255-byte file-name limit.
func TestLockWorktree_LongName(t *testing.T) {
	release, err := lockWorktree(t.TempDir(), strings.Repeat("機能", 30))
	if err != nil {
		t.Fatalf("lockWorktree: %v", err)
	}
	release()
}

// TestRemove_RefusesAWorktreesDirLinkedAboveTheRoot: a worktrees dir that is
// a symlink to the root's parent turned `remove --name <root's name>` into
// rm -rf of the monorepo root.
func TestRemove_RefusesAWorktreesDirLinkedAboveTheRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "mono")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(parent, filepath.Join(root, worktreesName)); err != nil {
		t.Fatal(err)
	}
	if _, err := worktreePathFor(root, "mono"); err == nil {
		t.Error("worktreePathFor resolved to the monorepo root through a symlinked worktrees dir")
	}
}

// TestRemove_RefusesAParentOfARegisteredWorktree: an older bough created
// nested names such as feat/x, and `remove --name feat` deleted feat/x and
// its datadir while its engines kept running.
func TestRemove_RefusesAParentOfARegisteredWorktree(t *testing.T) {
	root, _ := sameNameFixture(t, "feat")
	nested := filepath.Join(worktreesDir(root), "feat", "x")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".bough"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".bough", "ports.json"), []byte(`{"feat/x": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, args := range [][]string{{"--name", "feat"}, {"--name", "feat/y"}} {
		cmd := newRemoveCmd()
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Errorf("remove %v succeeded, want an error", args)
		}
	}
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("the registered nested worktree was touched: %v", err)
	}
	// The registered legacy name itself can still be removed.
	cmd := newRemoveCmd()
	cmd.SetArgs([]string{"--name", "feat/x"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Errorf("remove of the registered legacy name feat/x: %v", err)
	}
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Errorf("feat/x still exists after its own remove: %v", err)
	}
}

// caseInsensitive reports whether dir's filesystem folds letter case.
func caseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "Case-Probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(probe) }()
	_, err := os.Stat(filepath.Join(dir, "case-probe"))
	return err == nil
}

// TestRunRemove_RechecksTheCaseAliasUnderTheLock: a remove of F-AUTH passed
// the case check before F-Auth existed, then waited on the lock while a
// create made F-Auth, and went on to delete it. runRemove is called
// directly, as after a check that passed, with F-Auth already there.
func TestRunRemove_RechecksTheCaseAliasUnderTheLock(t *testing.T) {
	root, wt := sameNameFixture(t, "F-Auth")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if !caseInsensitive(t, root) {
		t.Skip("case-sensitive filesystem: F-AUTH is a different directory")
	}
	cfg := &config.Config{Registry: config.RegistryConfig{Path: ".bough/ports.json"}}
	alias := filepath.Join(worktreesDir(root), "F-AUTH")
	if err := runRemove(context.Background(), io.Discard, cfg, root, "F-AUTH", alias, 0); err == nil {
		t.Error("runRemove F-AUTH succeeded, want the case alias refused")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("F-Auth was deleted through its case alias: %v", err)
	}
}

// TestCreate_RechecksTheCaseAliasUnderTheLock is the create half: a create
// of F-AUTH that waited on the lock while F-Auth was created went on into
// F-Auth's directory under another registry name.
func TestCreate_RechecksTheCaseAliasUnderTheLock(t *testing.T) {
	root, wt := sameNameFixture(t, "F-Auth")
	if !caseInsensitive(t, root) {
		t.Skip("case-sensitive filesystem: F-AUTH is a different directory")
	}
	release, err := lockWorktree(root, "F-Auth")
	if err != nil {
		t.Fatal(err)
	}
	cmd := newCreateCmd()
	cmd.SetArgs([]string{"--name", "F-AUTH", "--cwd", root, "--no-fetch"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	time.Sleep(500 * time.Millisecond)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if err == nil {
			t.Error("create F-AUTH succeeded after F-Auth appeared, want the case alias refused")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("create never finished")
	}
	if _, err := os.Stat(filepath.Join(root, ".bough", "ports.json")); !os.IsNotExist(err) {
		t.Errorf("create reserved ports under the case alias: %v", err)
	}
}

// TestLockWorktree_CaseAliasesShareALock: names that differ only by case
// open one directory on a case-insensitive filesystem, so they must wait
// on one lock, long names (hashed) included.
func TestLockWorktree_CaseAliasesShareALock(t *testing.T) {
	root := t.TempDir()
	for _, pair := range [][2]string{{"F-Auth", "F-AUTH"}, {strings.Repeat("a", 201), strings.Repeat("A", 201)}} {
		release, err := lockWorktree(root, pair[0])
		if err != nil {
			t.Fatal(err)
		}
		acquired := make(chan func(), 1)
		go func() {
			second, err := lockWorktree(root, pair[1])
			if err == nil {
				acquired <- second
			}
		}()
		var second func()
		select {
		case second = <-acquired:
			t.Errorf("%.10s and %.10s took separate locks", pair[0], pair[1])
		case <-time.After(300 * time.Millisecond):
		}
		release()
		if second == nil {
			second = <-acquired
		}
		second()
	}
}

// TestRemove_RefusesATargetAboveTheRootByAPrefixedName: a directory name
// starting with ".." is not a parent step, and a string-prefix test let
// `remove --name ..parent` through a worktrees dir symlinked to the root's
// grandparent, deleting the root's parent.
func TestRemove_RefusesATargetAboveTheRootByAPrefixedName(t *testing.T) {
	grand := t.TempDir()
	root := filepath.Join(grand, "..parent", "..mono")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(grand, filepath.Join(root, worktreesName)); err != nil {
		t.Fatal(err)
	}
	if _, err := worktreePathFor(root, "..parent"); err == nil {
		t.Error("worktreePathFor resolved to the root's parent and was accepted")
	}
}

// TestRemove_RefusesASymlinkInsideANestedName: only the last component
// was checked, so feat/x with feat linked to Other removed Other/x.
func TestRemove_RefusesASymlinkInsideANestedName(t *testing.T) {
	root, _ := sameNameFixture(t, "feat")
	other := filepath.Join(worktreesDir(root), "Other", "x")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(worktreesDir(root), "Other"), filepath.Join(worktreesDir(root), "feat")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".bough"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".bough", "ports.json"), []byte(`{"feat/x": {}, "Other/x": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	cmd := newRemoveCmd()
	cmd.SetArgs([]string{"--name", "feat/x"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Error("remove feat/x through a symlinked feat succeeded")
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("Other/x was deleted: %v", err)
	}
}

// TestResolveRemoveTarget_NestedPath: a WorktreeRemove payload for a nested
// worktree an older bough created names <root>/worktrees/feat/x; its parent
// is not the worktrees dir, but the worktree is still removable.
func TestResolveRemoveTarget_NestedPath(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(worktreesDir(root), "feat", "x")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	gotRoot, name, path, err := resolveRemoveTarget("", nested)
	if err != nil || name != "feat/x" || path != nested || gotRoot != root {
		t.Errorf("resolveRemoveTarget = (%q, %q, %q, %v), want (%q, feat/x, %q, nil)", gotRoot, name, path, err, root, nested)
	}
}
