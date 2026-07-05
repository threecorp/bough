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
