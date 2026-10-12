//go:build darwin || linux

package dockerutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
