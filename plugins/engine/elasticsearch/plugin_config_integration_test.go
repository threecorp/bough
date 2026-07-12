//go:build integration && (darwin || linux)

// Integration test proving the elasticsearch-plugins.yml + config_mount
// mechanism end-to-end against a real Docker daemon: a plugin declared
// in `plugins:` actually installs (verified via `_cat/plugins`, not
// just "the container started"), and `extras["es.config_mount"]`
// actually bind-mounts a host-prepared directory into the container's
// config dir. Requires a real docker daemon; run with
// `go test -tags integration -timeout 5m -v ./plugins/engine/elasticsearch/... -run TestPluginConfigMechanism`.
package elasticsearch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"
)

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker not on PATH: %v", err)
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
}

func dockerExec(t *testing.T, container string, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", append([]string{"exec", container}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec %s %v: %v\n%s", container, args, err, out)
	}
	return string(out)
}

// TestPluginConfigMechanism_InstallsPluginAndMountsConfig proves both
// halves of the feature at once: an official plugin (analysis-icu —
// small and fast, no external location needed) actually installs via
// elasticsearch-plugins.yml, and a host directory bind-mounts via
// es.config_mount at the path es.config_mount_target names.
func TestPluginConfigMechanism_InstallsPluginAndMountsConfig(t *testing.T) {
	requireDocker(t)

	root := t.TempDir()
	engineProviderWorktree := filepath.Join(root, "auba-api")
	if err := os.MkdirAll(engineProviderWorktree, 0o755); err != nil {
		t.Fatalf("mkdir engine-provider worktree: %v", err)
	}

	// config_mount source: a host-prepared dir (standing in for a
	// pre-fetched analyzer dictionary), declared relative to the raw
	// worktree root exactly as an operator would in .bough.yaml.
	configMountRel := filepath.Join("auba-api", "es-config-fixture")
	configMountAbs := filepath.Join(root, configMountRel)
	if err := os.MkdirAll(configMountAbs, 0o755); err != nil {
		t.Fatalf("mkdir config_mount fixture: %v", err)
	}
	const markerContent = "bough-config-mount-fixture-marker"
	if err := os.WriteFile(filepath.Join(configMountAbs, "marker.txt"), []byte(markerContent), 0o644); err != nil {
		t.Fatalf("write marker file: %v", err)
	}

	const port = 59301 // fixed, distinctive — unlikely to collide with a developer's own worktrees
	datadir := filepath.Join(engineProviderWorktree, ".local", "elasticsearch-data")
	containerName := dockerContainerName(port)

	p := &Provider{}
	ctx := context.Background()
	upReq := &api.UpReq{
		Ports:        []api.PortSpec{{Role: "main", Port: port}},
		Datadir:      datadir,
		WorktreeRoot: engineProviderWorktree,
		Plugins:      []api.PluginSpec{{ID: "analysis-icu"}},
		Extras: map[string]string{
			"es.config_mount":        configMountRel,
			"es.config_mount_target": "/usr/share/elasticsearch/config/sudachi",
			"es.heap":                "512m", // keep the smoke test light
		},
	}

	if err := p.dockerUp(ctx, upReq); err != nil {
		t.Fatalf("dockerUp: %v", err)
	}
	t.Cleanup(func() {
		_ = p.dockerDown(ctx, &api.DownReq{Ports: []int{port}, GracefulTimeoutSec: 10})
	})

	ready, err := p.dockerReadyCheck(ctx, port, 300)
	if err != nil || !ready {
		t.Fatalf("dockerReadyCheck: ready=%v err=%v", ready, err)
	}

	// 1. elasticsearch-plugins.yml landed in the container's config dir
	// with the declared plugin.
	pluginsYAML := dockerExec(t, containerName, "cat", dockerConfigDir+"/"+pluginsYAMLFilename)
	if !strings.Contains(pluginsYAML, "id: analysis-icu") {
		t.Errorf("elasticsearch-plugins.yml in container missing analysis-icu:\n%s", pluginsYAML)
	}

	// 2. the plugin actually installed — not just "the file exists",
	// but ES's own idempotent install-on-boot mechanism ran.
	if err := waitForPluginInstalled(t, port, "analysis-icu", 120*time.Second); err != nil {
		t.Errorf("analysis-icu did not appear in _cat/plugins: %v", err)
	}

	// 3. config_mount landed at the custom target with the fixture's
	// real content (proves the bind, not just "a directory exists").
	marker := dockerExec(t, containerName, "cat", "/usr/share/elasticsearch/config/sudachi/marker.txt")
	if strings.TrimSpace(marker) != markerContent {
		t.Errorf("config_mount marker.txt = %q, want %q", strings.TrimSpace(marker), markerContent)
	}

	if err := p.dockerDown(ctx, &api.DownReq{Ports: []int{port}, GracefulTimeoutSec: 10}); err != nil {
		t.Fatalf("dockerDown: %v", err)
	}
	if err := exec.Command("docker", "inspect", containerName).Run(); err == nil {
		t.Errorf("container %s should have been removed by Down but still exists", containerName)
	}
}

// waitForPluginInstalled polls _cat/plugins?v (ES's own plugin-install
// pass runs during boot, so ReadyCheck succeeding does not guarantee
// the plugins.yml pass has finished writing its result to the
// registry the _cat API reads from).
func waitForPluginInstalled(t *testing.T, port int, pluginID string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/_cat/plugins?v", port)
	var lastBody string
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:gosec,noctx // fixed-port localhost dev/test call, no external input
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			lastBody = string(body)
			if strings.Contains(lastBody, pluginID) {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %s; last _cat/plugins body:\n%s", pluginID, lastBody)
}
