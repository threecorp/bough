//go:build integration && (darwin || linux)

package redis

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/ikeikeikeike/bough/pkg/dockerutil"
	"github.com/ikeikeikeike/bough/plugins/engine/api"
)

// TestDockerUp_ImageMismatchRecovery follows the remedy with real Redis data.
// Only containers created by this test are removed, by ID through t.Cleanup.
func TestDockerUp_ImageMismatchRecovery(t *testing.T) {
	for _, gone := range []bool{false, true} {
		t.Run(fmt.Sprintf("container gone=%v", gone), func(t *testing.T) {
			testDockerImageMismatchRecovery(t, gone)
		})
	}
}

func testDockerImageMismatchRecovery(t *testing.T, gone bool) {
	t.Helper()
	cli, err := dockerutil.NewClient()
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		t.Fatalf("docker daemon unreachable: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	name := dockerContainerName(port)
	if id, err := dockerutil.LookupByName(ctx, cli, name); err != nil || id != "" {
		t.Fatalf("test container name %s is unavailable: id=%s, err=%v", name, id, err)
	}

	datadir := filepath.Join(t.TempDir(), "redis data")
	if err := os.Mkdir(datadir, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(datadir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(datadir, ".old-data"), []byte("discard me"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Redis writes as its own uid on Linux; remove test data as root if needed.
	// Registered before container cleanup so no Redis process writes during it.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if err := dockerutil.RemoveDatadir(cleanupCtx, datadir); err != nil {
			t.Errorf("cleanup datadir: %v", err)
		}
	})
	backend := dockerBackend{}
	ports := []api.PortSpec{{Role: "main", Port: port}}
	if err := backend.Up(ctx, &api.UpReq{Ports: ports, Datadir: datadir, Extras: map[string]string{"version": "7"}}); err != nil {
		t.Fatalf("Up redis:7-alpine: %v", err)
	}
	oldID := redisRecoveryContainer(t, ctx, cli, name)
	if ready, err := backend.ReadyCheck(ctx, port, 30); err != nil || !ready {
		t.Fatalf("old Redis readiness: ready=%v, err=%v", ready, err)
	}

	oldImage, err := cli.ImageInspect(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := os.ReadFile(datadir + ".image")
	if err != nil || string(stamp) != "redis:7-alpine\n"+oldImage.ID+"\n" {
		t.Fatalf("first Up did not record its image: %q, %v", stamp, err)
	}
	if gone {
		if err := cli.ContainerRemove(ctx, oldID, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Fatal(err)
		}
	}

	req := &api.UpReq{Ports: ports, Datadir: datadir, Extras: map[string]string{"version": "8"}}
	err = backend.Up(ctx, req)
	var mismatch *dockerutil.ImageMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Up error = %v, want ImageMismatchError", err)
	}
	wantName := name
	if gone {
		wantName = ""
	}
	if mismatch.ContainerName != wantName || mismatch.HaveImage != "redis:7-alpine" || mismatch.WantImage != "redis:8-alpine" {
		t.Fatalf("mismatch = %+v", mismatch)
	}
	if gone && strings.Contains(err.Error(), "docker rm") {
		t.Fatalf("remedy removes a nonexistent container: %v", err)
	}
	for _, want := range []string{"delete the image record", datadir + ".image", datadir, "empty the data directory", "keep the directory itself", "all data in that directory is lost", "WorktreeCreate hook", "`bough create` with the same worktree name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("remedy missing %q: %v", want, err)
		}
	}
	if !gone {
		info, err := cli.ContainerInspect(ctx, oldID)
		if err != nil || info.State == nil || !info.State.Running || info.Config.Image != "redis:7-alpine" {
			t.Fatalf("mismatch changed the old container: info=%+v, err=%v", info, err)
		}
		if err := cli.ContainerRemove(ctx, oldID, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Fatal(err)
		}
	}
	emptyRedisRecoveryDatadir(t, ctx, cli, datadir)
	after, err := os.Stat(datadir)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("recovery did not keep the directory itself: %v", err)
	}
	entries, err := os.ReadDir(datadir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("recovery did not empty all data: entries=%v, err=%v", entries, err)
	}
	if err := os.Remove(datadir + ".image"); err != nil {
		t.Fatal(err)
	}
	if err := backend.Up(ctx, req); err != nil {
		t.Fatalf("Up redis:8-alpine after recovery: %v", err)
	}
	newID := redisRecoveryContainer(t, ctx, cli, name)
	info, err := cli.ContainerInspect(ctx, newID)
	if err != nil || newID == oldID || info.Config.Image != "redis:8-alpine" || info.State == nil || !info.State.Running {
		t.Fatalf("recovered container: info=%+v, err=%v", info, err)
	}
	newImage, err := cli.ImageInspect(ctx, "redis:8-alpine")
	if err != nil {
		t.Fatal(err)
	}
	stamp, err = os.ReadFile(datadir + ".image")
	if err != nil || string(stamp) != "redis:8-alpine\n"+newImage.ID+"\n" {
		t.Fatalf("recovery did not update image record: %q, %v", stamp, err)
	}
	if ready, err := backend.ReadyCheck(ctx, port, 30); err != nil || !ready {
		t.Fatalf("new Redis readiness: ready=%v, err=%v", ready, err)
	}
}

func redisRecoveryContainer(t *testing.T, ctx context.Context, cli *client.Client, name string) string {
	t.Helper()
	id, err := dockerutil.LookupByName(ctx, cli, name)
	if err != nil || id == "" {
		t.Fatalf("test container %s missing: %v", name, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := cli.ContainerRemove(cleanupCtx, id, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			t.Errorf("cleanup test container %s: %v", id, err)
		}
	})
	return id
}

func emptyRedisRecoveryDatadir(t *testing.T, ctx context.Context, cli *client.Client, datadir string) {
	t.Helper()
	// A root container can delete Redis-owned files on Linux. find also removes
	// dotfiles, while its depth limits preserve the bind-mounted directory.
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:  "redis:7-alpine",
		User:   "0:0",
		Cmd:    []string{"find", "/data", "-mindepth", "1", "-maxdepth", "1", "-exec", "rm", "-rf", "--", "{}", "+"},
		Labels: map[string]string{"com.bough.test": "redis-recovery"},
	}, &container.HostConfig{Binds: []string{datadir + ":/data"}}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true}) })
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	waitC, errC := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case result := <-waitC:
		if result.StatusCode != 0 {
			t.Fatalf("empty datadir exited %d: %v", result.StatusCode, result.Error)
		}
	case err := <-errC:
		t.Fatalf("wait for empty datadir: %v", err)
	}
}
