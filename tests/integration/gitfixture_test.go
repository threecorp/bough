//go:build integration

package integration

import (
	"os"
	"os/exec"
	"testing"
)

// gitInit materialises a minimal git repo with one commit on develop so
// `bough create` can `git worktree add` against it. The init / commit
// pair is the smallest reproducer that satisfies DetectBase + the
// branch_strategy = develop schema field.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=bough-test",
			"GIT_AUTHOR_EMAIL=test@bough.invalid",
			"GIT_COMMITTER_NAME=bough-test",
			"GIT_COMMITTER_EMAIL=test@bough.invalid",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "develop")
	run("commit", "--allow-empty", "-m", "init")
}
