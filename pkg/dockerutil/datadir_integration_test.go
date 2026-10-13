//go:build integration && (darwin || linux)

package dockerutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// newEngineDatadir lays out <tmp>/.local/mysql-data the way a worktree gets
// it on Linux: the daemon creates both dirs as root for the bind mount, and
// the engine's entrypoint chowns the datadir to its uid (999) and writes a
// 0700 subtree. The host user can unlink none of it.
func newEngineDatadir(t *testing.T) (root, dir string) {
	t.Helper()
	cli := newTestClient(t)
	t.Cleanup(func() { _ = cli.Close() }) // runs after the container removal below
	pullTestImage(t, cli)

	// Not t.TempDir(): if RemoveDatadir regressed, the harness's own
	// RemoveAll would fail too and hide which of the two broke.
	root, err := os.MkdirTemp("", "bough-dockerutil-datadir-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = RemoveDatadir(context.Background(), root) })

	ctx := context.Background()
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: testImage,
		User:  "0:0",
		Cmd: []string{"sh", "-c", "mkdir -p /w/.local/mysql-data/a/b && echo x > /w/.local/mysql-data/a/b/f && " +
			"touch /w/.local/mysql-data/.hidden && printf 'mysql:8.4\\n' > /w/.local/mysql-data.image && chown -R 999:999 /w/.local/mysql-data && chmod -R 0700 /w/.local/mysql-data/a"},
		Labels: testLabels,
	}, &container.HostConfig{Binds: []string{root + ":/w"}}, nil, nil, "")
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	})
	if err := runToExit(ctx, cli, resp.ID, "preparing test data"); err != nil {
		t.Fatalf("writer: %v", err)
	}
	return root, filepath.Join(root, ".local", "mysql-data")
}

// On Linux this goes through the root-container fallback; on macOS the
// daemon maps the uid and the first RemoveAll succeeds.
func TestRemoveDatadir_RemovesAContainerOwnedTree(t *testing.T) {
	root, dir := newEngineDatadir(t)
	if err := RemoveDatadir(context.Background(), dir); err != nil {
		t.Fatalf("RemoveDatadir: %v", err)
	}
	for _, path := range []string{dir, dir + ".image"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cleanup left %s: %v", path, err)
		}
	}
	// What the host does next in bough remove: rm -rf the worktree.
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("worktree removal after Cleanup: %v", err)
	}
}

// Drives the fallback directly so it runs on macOS too.
func TestRemoveAsRoot_DeletesTheDir(t *testing.T) {
	_, dir := newEngineDatadir(t)
	if err := removeAsRoot(context.Background(), dir); err != nil {
		t.Fatalf("removeAsRoot: %v", err)
	}
	for _, path := range []string{dir, dir + ".image"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("root cleanup left %s: %v", path, err)
		}
	}
}

func TestRecordDatadirImage_ContainerOwnedParent(t *testing.T) {
	_, dir := newEngineDatadir(t)
	cli := newTestClient(t)
	defer func() { _ = cli.Close() }()
	img, err := cli.ImageInspect(context.Background(), testImage)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordDatadirImage(context.Background(), cli, "bough-record-test-absent", dir, testImage); err != nil {
		t.Fatalf("record in container-owned parent: %v", err)
	}
	data, err := os.ReadFile(dir + ".image")
	if err != nil || string(data) != testImage+"\n"+img.ID+"\n" {
		t.Fatalf("record = %q, err=%v", data, err)
	}
	if err := RemoveDatadir(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir + ".image"); !os.IsNotExist(err) {
		t.Fatalf("image record left behind: %v", err)
	}
}

func TestRemoveAsRoot_SupportsSymlinkedParent(t *testing.T) {
	root, dir := newEngineDatadir(t)
	parentLink := filepath.Join(root, "linked-local")
	if err := os.Symlink(filepath.Dir(dir), parentLink); err != nil {
		t.Fatal(err)
	}
	if err := removeAsRoot(context.Background(), filepath.Join(parentLink, filepath.Base(dir))); err != nil {
		t.Fatalf("cleanup through a symlinked parent: %v", err)
	}
	for _, path := range []string{dir, dir + ".image"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("cleanup left %s: %v", path, err)
		}
	}
}
