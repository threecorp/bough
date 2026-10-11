package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

// TestWorktreePathFor pins the guard every name-based entry point shares: a
// name that resolves to the worktrees dir itself or outside it ("..",
// "../x") is refused, because remove would otherwise rm -rf the monorepo
// root or a sibling of it.
func TestWorktreePathFor(t *testing.T) {
	root := t.TempDir()
	dir := worktreesDir(root)
	for _, name := range []string{"F-Auth", "feat/x"} {
		got, err := worktreePathFor(root, name)
		if err != nil || got != filepath.Join(dir, name) {
			t.Errorf("worktreePathFor(%q) = (%q, %v), want %q", name, got, err, filepath.Join(dir, name))
		}
	}
	for _, name := range []string{"", ".", "..", "../x", "a/../..", "../worktrees-x"} {
		if got, err := worktreePathFor(root, name); err == nil {
			t.Errorf("worktreePathFor(%q) = %q, want an error", name, got)
		}
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
