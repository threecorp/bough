//go:build darwin || linux

package dockerutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureDatadir_CreatesTheDirAndItsParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".local", "redis-data")
	if err := EnsureDatadir(dir); err != nil {
		t.Fatalf("EnsureDatadir: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("datadir not created: %v", err)
	}
}

// A worktree from an earlier version can carry a root-owned .local; Up must
// still go ahead and let the daemon create the datadir there.
func TestEnsureDatadir_LeavesAnUnwritableParentToTheDaemon(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	parent := filepath.Join(t.TempDir(), ".local")
	if err := os.Mkdir(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	if err := EnsureDatadir(filepath.Join(parent, "redis-data")); err != nil {
		t.Fatalf("EnsureDatadir: %v", err)
	}
}

func TestEnsureDatadir_ReportsOtherErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".local")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDatadir(filepath.Join(file, "redis-data")); err == nil {
		t.Fatal("EnsureDatadir succeeded under a regular file")
	}
}

func TestRemoveDatadir_RemovesAHostOwnedTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mysql-data")
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "b", "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveDatadir(context.Background(), dir); err != nil {
		t.Fatalf("RemoveDatadir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("datadir still present: %v", err)
	}
}

func TestRemoveDatadir_MissingDirIsNotAnError(t *testing.T) {
	if err := RemoveDatadir(context.Background(), filepath.Join(t.TempDir(), "never-created")); err != nil {
		t.Fatalf("RemoveDatadir: %v", err)
	}
}

// A symlinked datadir in a parent the host cannot write must not reach the
// root container: the bind mount would follow the link and delete its target.
func TestRemoveDatadir_RefusesASymlinkedDatadir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	tmp := t.TempDir()
	target := filepath.Join(tmp, "elsewhere")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(tmp, ".local")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "mysql-data")
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	err := RemoveDatadir(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("RemoveDatadir = %v, want a refusal", err)
	}
	if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
		t.Fatalf("symlink target was touched: %v", err)
	}
}

// A permission error the root container cannot clear either (no daemon
// here) must surface both causes, so the operator sees why the datadir stayed.
func TestRemoveDatadir_ReportsBothErrorsWhenTheRootContainerFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	dir := filepath.Join(t.TempDir(), "mysql-data")
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	err := RemoveDatadir(context.Background(), dir)
	if err == nil {
		t.Fatal("RemoveDatadir succeeded on a tree it could not delete")
	}
	for _, want := range []string{"permission denied", "root container"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
