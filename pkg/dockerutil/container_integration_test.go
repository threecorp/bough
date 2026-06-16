//go:build integration && (darwin || linux)

// Integration tests for the daemon-dependent helpers in container.go.
// Only run with `go test -tags integration ./pkg/dockerutil/...`.
//
// Each test allocates a throwaway `alpine:3.20` container with the
// label `com.bough.test=dockerutil` for blast-radius bounding, then
// teardown is guaranteed via t.Cleanup.

package dockerutil

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

const (
	testImage = "alpine:3.20"
	testLabel = "com.bough.test=dockerutil"
)

func newTestClient(t *testing.T) *client.Client {
	t.Helper()
	cli, err := NewClient()
	if err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}
	return cli
}

func pullTestImage(t *testing.T, cli *client.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := PullIfMissing(ctx, cli, testImage); err != nil {
		t.Fatalf("PullIfMissing(%s): %v", testImage, err)
	}
}

func createSleepContainer(t *testing.T, cli *client.Client, name string, start bool) string {
	t.Helper()
	ctx := context.Background()
	cfg := &container.Config{
		Image:  testImage,
		Cmd:    []string{"sleep", "3600"},
		Labels: map[string]string{"com.bough.test": "dockerutil"},
	}
	resp, err := cli.ContainerCreate(ctx, cfg, &container.HostConfig{}, nil, nil, name)
	if err != nil {
		t.Fatalf("ContainerCreate(%s): %v", name, err)
	}
	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	})
	if start {
		if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
			t.Fatalf("ContainerStart(%s): %v", name, err)
		}
	}
	return resp.ID
}

func TestLookupByName_Found(t *testing.T) {
	cli := newTestClient(t)
	defer cli.Close()
	pullTestImage(t, cli)

	name := fmt.Sprintf("bough-test-lookup-%d", time.Now().UnixNano())
	wantID := createSleepContainer(t, cli, name, true)

	gotID, err := LookupByName(context.Background(), cli, name)
	if err != nil {
		t.Fatalf("LookupByName: %v", err)
	}
	if gotID != wantID {
		t.Errorf("LookupByName(%s) = %s, want %s", name, gotID, wantID)
	}
}

func TestLookupByName_NotFound(t *testing.T) {
	cli := newTestClient(t)
	defer cli.Close()

	name := fmt.Sprintf("bough-test-missing-%d", time.Now().UnixNano())
	gotID, err := LookupByName(context.Background(), cli, name)
	if err != nil {
		t.Fatalf("LookupByName: %v", err)
	}
	if gotID != "" {
		t.Errorf("LookupByName(%s) = %s, want empty", name, gotID)
	}
}

func TestRemoveIfExists_Idempotent(t *testing.T) {
	cli := newTestClient(t)
	defer cli.Close()
	pullTestImage(t, cli)

	name := fmt.Sprintf("bough-test-remove-%d", time.Now().UnixNano())
	_ = createSleepContainer(t, cli, name, true)

	if err := RemoveIfExists(context.Background(), cli, name); err != nil {
		t.Fatalf("RemoveIfExists first call: %v", err)
	}
	// Second call must be a no-op now that nothing matches the name.
	if err := RemoveIfExists(context.Background(), cli, name); err != nil {
		t.Errorf("RemoveIfExists second call (no-op): %v", err)
	}
	id, _ := LookupByName(context.Background(), cli, name)
	if id != "" {
		t.Errorf("container still exists after RemoveIfExists: %s", id)
	}
}

func TestUpOrReuse_SkipsRunning(t *testing.T) {
	cli := newTestClient(t)
	defer cli.Close()
	pullTestImage(t, cli)

	name := fmt.Sprintf("bough-test-reuse-%d", time.Now().UnixNano())
	_ = createSleepContainer(t, cli, name, true)

	skip, err := UpOrReuse(context.Background(), cli, name)
	if err != nil {
		t.Fatalf("UpOrReuse: %v", err)
	}
	if !skip {
		t.Errorf("UpOrReuse skip = false, want true (container is running)")
	}
}

func TestUpOrReuse_RemovesStopped(t *testing.T) {
	cli := newTestClient(t)
	defer cli.Close()
	pullTestImage(t, cli)

	name := fmt.Sprintf("bough-test-stale-%d", time.Now().UnixNano())
	// Create-but-not-start = stopped container, mimicking a partial
	// Up failure.
	_ = createSleepContainer(t, cli, name, false)

	skip, err := UpOrReuse(context.Background(), cli, name)
	if err != nil {
		t.Fatalf("UpOrReuse: %v", err)
	}
	if skip {
		t.Errorf("UpOrReuse skip = true, want false (container was stopped)")
	}
	id, _ := LookupByName(context.Background(), cli, name)
	if id != "" {
		t.Errorf("stale container still exists after UpOrReuse: %s", id)
	}
}
