//go:build integration && (darwin || linux)

package dockerutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// writeAsEngineUID fills dir the way an engine container does: as uid 999
// (mysql / postgres / redis), with a 0700 subtree. On Linux the host user
// cannot unlink the result, which is the case RemoveDatadir exists for.
func writeAsEngineUID(t *testing.T, dir string) {
	t.Helper()
	cli := newTestClient(t)
	t.Cleanup(func() { _ = cli.Close() }) // runs after the container removal below
	pullTestImage(t, cli)
	ctx := context.Background()
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:  testImage,
		User:   "999:999",
		Cmd:    []string{"sh", "-c", "mkdir -p /d/a/b && echo x > /d/a/b/f && touch /d/.hidden && chmod -R 0700 /d/a"},
		Labels: testLabels,
	}, &container.HostConfig{Binds: []string{dir + ":/d"}}, nil, nil, "")
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	})
	waitC, errC := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNextExit)
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		t.Fatalf("ContainerStart: %v", err)
	}
	select {
	case w := <-waitC:
		if w.StatusCode != 0 {
			t.Fatalf("writer exited %d", w.StatusCode)
		}
	case err := <-errC:
		t.Fatalf("ContainerWait: %v", err)
	}
}

func newEngineDatadir(t *testing.T) string {
	t.Helper()
	// Not t.TempDir(): if RemoveDatadir regressed, the harness's own
	// RemoveAll would fail too and hide which of the two broke.
	parent, err := os.MkdirTemp("", "bough-dockerutil-datadir-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = RemoveDatadir(context.Background(), parent) })
	dir := filepath.Join(parent, "mysql-data")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAsEngineUID(t, dir)
	return dir
}

// On Linux this goes through the root-container fallback; on macOS the
// daemon maps the uid and the first RemoveAll succeeds.
func TestRemoveDatadir_RemovesAContainerOwnedTree(t *testing.T) {
	dir := newEngineDatadir(t)
	if err := RemoveDatadir(context.Background(), dir); err != nil {
		t.Fatalf("RemoveDatadir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("datadir still present: %v", err)
	}
}

// Drives the fallback directly so it runs on macOS too.
func TestEmptyAsRoot_LeavesTheDirEmpty(t *testing.T) {
	dir := newEngineDatadir(t)
	if err := emptyAsRoot(context.Background(), dir); err != nil {
		t.Fatalf("emptyAsRoot: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("emptyAsRoot left %d entries", len(entries))
	}
}
