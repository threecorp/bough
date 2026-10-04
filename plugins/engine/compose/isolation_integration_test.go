//go:build integration && (darwin || linux)

// Integration test proving the core worktree-isolation claim: two
// separate "worktrees" (independent directory trees, each carrying
// its own copy of a textually-identical compose file) must never
// collide when both are brought up at the same time — distinct
// project names, distinct container names, both independently
// reachable. Requires a real docker compose CLI + daemon; run with
// `go test -tags integration ./plugins/engine/compose/...`.
package compose

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"
)

const isolationFixtureYAML = `services:
  redis:
    image: redis:7-alpine
`

func requireDockerCompose(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker not on PATH: %v", err)
	}
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skipf("docker compose not available: %v", err)
	}
}

// setupWorktree creates <root>/<repoName>/compose.yml, mimicking one
// worktree's own checkout of a repo that carries its own compose
// file. root is a fresh t.TempDir() per call, so worktree A and B are
// always physically distinct directories even with identical content.
func setupWorktree(t *testing.T, repoName string) (repoDir string) {
	t.Helper()
	root := t.TempDir()
	repoDir = filepath.Join(root, repoName)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "compose.yml"), []byte(isolationFixtureYAML), 0o644); err != nil {
		t.Fatalf("write compose.yml: %v", err)
	}
	return repoDir
}

func assertContainerRunning(t *testing.T, name string) {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", name).Output()
	if err != nil {
		t.Fatalf("docker inspect %s: %v", name, err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		t.Errorf("container %s is not running: %s", name, out)
	}
}

func assertContainerGone(t *testing.T, name string) {
	t.Helper()
	if err := exec.Command("docker", "inspect", name).Run(); err == nil {
		t.Errorf("container %s should have been removed by Down but still exists", name)
	}
}

func TestWorktreeIsolation_TwoWorktreesSameComposeFile(t *testing.T) {
	requireDockerCompose(t)

	const repoName = "demo-api"
	repoA := setupWorktree(t, repoName)
	repoB := setupWorktree(t, repoName)

	pA, pB := New(), New()
	ctx := context.Background()

	upReq := func(repoDir string, port int) *api.UpReq {
		return &api.UpReq{
			WorktreeRoot: repoDir,
			Ports:        []api.PortSpec{{Role: "main", Port: port}},
			Extras: map[string]string{
				// Relative to the RAW worktree root (repoDir's parent) —
				// the same convention production configs use.
				"compose.file":        filepath.Join(repoName, "compose.yml"),
				"compose.service":     "redis",
				"compose.target_port": "6379",
			},
		}
	}
	// Fixed, distinct ports rather than dynamically allocated ones:
	// this test's whole point is to prove isolation given the SAME
	// compose file content, not to exercise bough's allocator.
	const portA, portB = 59101, 59102

	if err := pA.Up(ctx, upReq(repoA, portA)); err != nil {
		t.Fatalf("worktree A Up: %v", err)
	}
	t.Cleanup(func() {
		_ = pA.Down(ctx, &api.DownReq{Ports: []int{portA}, WorktreeRoot: repoA, GracefulTimeoutSec: 10})
	})

	if err := pB.Up(ctx, upReq(repoB, portB)); err != nil {
		t.Fatalf("worktree B Up: %v", err)
	}
	t.Cleanup(func() {
		_ = pB.Down(ctx, &api.DownReq{Ports: []int{portB}, WorktreeRoot: repoB, GracefulTimeoutSec: 10})
	})

	// Both must be independently reachable on their OWN ports at the
	// same time — the entire point of worktree isolation. Using pA to
	// probe portB (and vice versa would work identically) confirms
	// ReadyCheck's plain-TCP default needs no state from whichever
	// Provider instance happened to call Up() for that port.
	for _, port := range []int{portA, portB} {
		ok, err := pA.ReadyCheck(ctx, []int{port}, 30)
		if err != nil || !ok {
			t.Errorf("port %d not ready: ok=%v err=%v", port, ok, err)
		}
	}

	nameA := fmt.Sprintf("bough-compose-%d", portA)
	nameB := fmt.Sprintf("bough-compose-%d", portB)
	if nameA == nameB {
		t.Fatalf("container names must differ: both %q", nameA)
	}
	assertContainerRunning(t, nameA)
	assertContainerRunning(t, nameB)

	// Tearing down A must not affect B — proves Down is genuinely
	// service/project-scoped, not accidentally broad.
	if err := pA.Down(ctx, &api.DownReq{Ports: []int{portA}, WorktreeRoot: repoA, GracefulTimeoutSec: 10}); err != nil {
		t.Fatalf("worktree A Down: %v", err)
	}
	assertContainerGone(t, nameA)
	assertContainerRunning(t, nameB)
}
