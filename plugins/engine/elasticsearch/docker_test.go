//go:build darwin || linux

package elasticsearch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"
)

// TestBuildDockerEnv_PublishHostAndPort is the regression guard for
// v0.2.6. Before the fix the elasticsearch container's env did not
// include `network.publish_host` / `http.publish_port`, so ES
// advertised its container-internal bridge IP (e.g. 172.17.0.4:9200)
// via `_nodes/http`. Sniffing clients (olivere/elastic et al.) then
// dialed 172.17.0.4 from the host and crashed auba-api at boot with
// `no Elasticsearch node available`.
//
// The fix injects both lines verbatim; this test will fail loudly if
// either line is removed or renamed.
func TestBuildDockerEnv_PublishHostAndPort(t *testing.T) {
	got := buildDockerEnv("1g", "56205")

	mustHave := []string{
		"network.host=0.0.0.0",
		"network.publish_host=127.0.0.1",
		"http.publish_port=56205",
	}
	for _, want := range mustHave {
		if !slices.Contains(got, want) {
			t.Errorf("buildDockerEnv missing %q\nfull env:\n%s",
				want, strings.Join(got, "\n  "))
		}
	}

	// And the existing must-have lines stay (= guard against an over-
	// eager refactor that drops the dev-mode disk threshold env).
	stable := []string{
		"discovery.type=single-node",
		"xpack.security.enabled=false",
		"cluster.routing.allocation.disk.threshold_enabled=false",
		"ES_JAVA_OPTS=-Xms1g -Xmx1g",
	}
	for _, want := range stable {
		if !slices.Contains(got, want) {
			t.Errorf("buildDockerEnv missing pre-fix env %q\nfull env:\n%s",
				want, strings.Join(got, "\n  "))
		}
	}
}

// TestBuildDockerEnv_HeapOverride confirms the JVM heap arg honours
// the caller's choice. Bough's `pickHeap` reads `extras["es.heap"]`,
// so a project running 5+ worktrees in parallel can shrink each node
// to 512m without forking the plugin.
func TestBuildDockerEnv_HeapOverride(t *testing.T) {
	got := buildDockerEnv("512m", "56205")
	want := "ES_JAVA_OPTS=-Xms512m -Xmx512m"
	if !slices.Contains(got, want) {
		t.Errorf("buildDockerEnv heap override = absent, want %q\nfull env:\n%s",
			want, strings.Join(got, "\n  "))
	}
}

// TestPickHeap exercises pickHeap directly — the extras["es.heap"]
// override + the "1g" default. TestBuildDockerEnv_HeapOverride's docstring
// advertises this extras path but never invokes pickHeap (it calls
// buildDockerEnv("512m", …) directly), so the actual extraction was
// untested; a mistyped key or broken default would have stayed green.
func TestPickHeap(t *testing.T) {
	cases := []struct {
		name string
		req  *api.UpReq
		want string
	}{
		{"override via extras", &api.UpReq{Extras: map[string]string{"es.heap": "512m"}}, "512m"},
		{"empty extras value → 1g default", &api.UpReq{Extras: map[string]string{"es.heap": ""}}, "1g"},
		{"nil extras → 1g default", &api.UpReq{}, "1g"},
	}
	for _, c := range cases {
		if got := pickHeap(c.req); got != c.want {
			t.Errorf("%s: pickHeap = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestPickMemoryLimitBytes exercises the extras["es.mem_limit"]
// override, the 2x-heap default derivation, and the invalid-input error
// paths for both.
func TestPickMemoryLimitBytes(t *testing.T) {
	cases := []struct {
		name    string
		req     *api.UpReq
		heap    string
		want    int64
		wantErr bool
	}{
		{"override via extras", &api.UpReq{Extras: map[string]string{"es.mem_limit": "3g"}}, "1g", 3 * 1024 * 1024 * 1024, false},
		{"no override, 1g heap → 2x heap (>= heap+1GiB)", &api.UpReq{}, "1g", 2 * 1024 * 1024 * 1024, false},
		{"no override, 512m heap → heap+1GiB headroom floor beats 2x", &api.UpReq{}, "512m", 512*1024*1024 + 1024*1024*1024, false},
		{"no override, 2g heap → 2x heap (4GiB) beats heap+1GiB", &api.UpReq{}, "2g", 4 * 1024 * 1024 * 1024, false},
		{"empty override falls back to default", &api.UpReq{Extras: map[string]string{"es.mem_limit": ""}}, "1g", 2 * 1024 * 1024 * 1024, false},
		{"override equal to heap is allowed", &api.UpReq{Extras: map[string]string{"es.mem_limit": "1g"}}, "1g", 1024 * 1024 * 1024, false},
		{"override below heap is rejected", &api.UpReq{Extras: map[string]string{"es.mem_limit": "512m"}}, "1g", 0, true},
		{"invalid override", &api.UpReq{Extras: map[string]string{"es.mem_limit": "not-a-size"}}, "1g", 0, true},
		{"zero override is rejected cleanly", &api.UpReq{Extras: map[string]string{"es.mem_limit": "0"}}, "1g", 0, true},
		{"invalid heap with no override", &api.UpReq{}, "not-a-size", 0, true},
		{"zero heap is rejected cleanly", &api.UpReq{}, "0", 0, true},
	}
	for _, c := range cases {
		got, err := pickMemoryLimitBytes(c.req, c.heap)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: pickMemoryLimitBytes(heap=%q) = %d, nil, want an error", c.name, c.heap, got)
			} else if strings.Contains(err.Error(), "%!w(<nil>)") {
				// Regression guard: the non-positive-size branches must not
				// fmt.Errorf("...%w", nilErr) — RAMInBytes returns (0, nil)
				// for "0", which used to produce a garbled ': %!w(<nil>)'.
				t.Errorf("%s: error contains a nil-%%w artifact: %v", c.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: pickMemoryLimitBytes(heap=%q) unexpected error: %v", c.name, c.heap, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: pickMemoryLimitBytes(heap=%q) = %d, want %d", c.name, c.heap, got, c.want)
		}
	}
}

// TestWritePluginsYAML_EmptyIsNoop confirms an engine with no
// `plugins:` declared gets no elasticsearch-plugins.yml at all — the
// pre-existing behaviour (no bind-mount, no file) must be unchanged.
func TestWritePluginsYAML_EmptyIsNoop(t *testing.T) {
	dir := t.TempDir()
	datadir := filepath.Join(dir, "es-data")
	got, err := writePluginsYAML(datadir, nil)
	if err != nil {
		t.Fatalf("writePluginsYAML(nil) unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("writePluginsYAML(nil) = %q, want empty path", got)
	}
	if _, err := os.Stat(filepath.Join(dir, pluginsYAMLFilename)); !os.IsNotExist(err) {
		t.Errorf("writePluginsYAML(nil) must not create %s, stat err = %v", pluginsYAMLFilename, err)
	}
}

// TestWritePluginsYAML_RendersOfficialAndUnofficialPlugins confirms the
// generated YAML matches elasticsearch-plugins.yml's own documented
// shape: an official plugin (id only) and an unofficial one (id +
// location), written next to datadir rather than inside it.
func TestWritePluginsYAML_RendersOfficialAndUnofficialPlugins(t *testing.T) {
	dir := t.TempDir()
	datadir := filepath.Join(dir, "es-data")
	plugins := []api.PluginSpec{
		{ID: "analysis-icu"},
		{ID: "analysis-sudachi", Location: "https://example.com/sudachi.zip"},
	}
	got, err := writePluginsYAML(datadir, plugins)
	if err != nil {
		t.Fatalf("writePluginsYAML: %v", err)
	}
	wantPath := filepath.Join(dir, pluginsYAMLFilename)
	if got != wantPath {
		t.Errorf("writePluginsYAML path = %q, want %q", got, wantPath)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	content := string(data)
	mustContain := []string{
		"id: analysis-icu",
		"id: analysis-sudachi",
		"location: https://example.com/sudachi.zip",
	}
	for _, want := range mustContain {
		if !strings.Contains(content, want) {
			t.Errorf("elasticsearch-plugins.yml missing %q\nfull content:\n%s", want, content)
		}
	}
	if strings.Contains(content, `location: ""`) {
		t.Errorf("elasticsearch-plugins.yml must omit empty location for official plugins\nfull content:\n%s", content)
	}
}

// TestResolveConfigMount covers: unset (no-op), absolute path,
// worktree-root-relative resolution (mirroring kind: compose's
// compose.file), a custom container target, and the two error paths
// (missing source dir, relative path with no WorktreeRoot).
func TestResolveConfigMount(t *testing.T) {
	base := t.TempDir()
	rawWorktreeRoot := filepath.Join(base, "F-Feature")
	engineProviderWorktree := filepath.Join(rawWorktreeRoot, "auba-api")
	sudachiDir := filepath.Join(rawWorktreeRoot, "auba-api", "es-config", "sudachi")
	if err := os.MkdirAll(sudachiDir, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}

	t.Run("unset is a no-op", func(t *testing.T) {
		got, err := resolveConfigMount(&api.UpReq{WorktreeRoot: engineProviderWorktree})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("resolveConfigMount(unset) = %q, want empty", got)
		}
	})

	t.Run("relative path resolves against the raw worktree root", func(t *testing.T) {
		req := &api.UpReq{
			WorktreeRoot: engineProviderWorktree,
			Extras:       map[string]string{"es.config_mount": "auba-api/es-config/sudachi"},
		}
		got, err := resolveConfigMount(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := sudachiDir + ":" + dockerConfigDir + "/sudachi:ro"
		if got != want {
			t.Errorf("resolveConfigMount = %q, want %q", got, want)
		}
	})

	t.Run("absolute path is used as-is", func(t *testing.T) {
		req := &api.UpReq{
			WorktreeRoot: engineProviderWorktree,
			Extras:       map[string]string{"es.config_mount": sudachiDir},
		}
		got, err := resolveConfigMount(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := sudachiDir + ":" + dockerConfigDir + "/sudachi:ro"
		if got != want {
			t.Errorf("resolveConfigMount = %q, want %q", got, want)
		}
	})

	t.Run("custom container target overrides the default basename", func(t *testing.T) {
		req := &api.UpReq{
			WorktreeRoot: engineProviderWorktree,
			Extras: map[string]string{
				"es.config_mount":        sudachiDir,
				"es.config_mount_target": "/usr/share/elasticsearch/config/custom",
			},
		}
		got, err := resolveConfigMount(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := sudachiDir + ":/usr/share/elasticsearch/config/custom:ro"
		if got != want {
			t.Errorf("resolveConfigMount = %q, want %q", got, want)
		}
	})

	t.Run("missing source directory errors", func(t *testing.T) {
		req := &api.UpReq{
			WorktreeRoot: engineProviderWorktree,
			Extras:       map[string]string{"es.config_mount": filepath.Join(rawWorktreeRoot, "does-not-exist")},
		}
		if _, err := resolveConfigMount(req); err == nil {
			t.Error("resolveConfigMount(nonexistent dir) = nil error, want an error")
		}
	})

	t.Run("relative path with empty WorktreeRoot errors", func(t *testing.T) {
		req := &api.UpReq{
			Extras: map[string]string{"es.config_mount": "auba-api/es-config/sudachi"},
		}
		if _, err := resolveConfigMount(req); err == nil {
			t.Error("resolveConfigMount(relative, no WorktreeRoot) = nil error, want an error")
		}
	})
}

// TestDatadirOwnedBy covers the detection mechanism at the heart of the
// issue #74 fix: recognising that a bind-mount datadir is already owned
// by the container uid, so Up neither chowns nor chmods a directory the
// container can already write. Exercised against the current uid as a
// stand-in for the container uid, because a test cannot portably create
// a 1000-owned directory (that needs root, and as root chown succeeds so
// the guarded branch is never reached).
func TestDatadirOwnedBy(t *testing.T) {
	dir := t.TempDir() // owned by the current uid
	self := uint32(os.Getuid())

	if !datadirOwnedBy(dir, self) {
		t.Errorf("datadirOwnedBy(self-owned dir, %d) = false, want true", self)
	}
	if datadirOwnedBy(dir, self+1) {
		t.Errorf("datadirOwnedBy(self-owned dir, %d) = true, want false (different uid)", self+1)
	}
	if datadirOwnedBy(filepath.Join(dir, "does-not-exist"), self) {
		t.Errorf("datadirOwnedBy(nonexistent path) = true, want false")
	}
}

// TestEnsureDatadirWritableByContainer_ChmodFallback exercises the
// non-root path where the datadir is owned by the caller (not the
// container uid): chown to 1000 EPERMs, the dir is not container-owned,
// and the chmod-0o777 fallback succeeds because the caller owns it — so
// Up must not error and the mode must be widened.
func TestEnsureDatadirWritableByContainer_ChmodFallback(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root — chown to the container uid succeeds; the fallback is not exercised")
	}
	if os.Getuid() == esContainerUID {
		t.Skip("running as the container uid — the ownership short-circuit applies, not the fallback")
	}
	dir := t.TempDir()
	if err := ensureDatadirWritableByContainer(dir); err != nil {
		t.Fatalf("ensureDatadirWritableByContainer(caller-owned dir) = %v, want nil (chmod fallback)", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o777 {
		t.Errorf("datadir mode = %o, want 0o777 after the fallback", info.Mode().Perm())
	}
}
