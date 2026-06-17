//go:build darwin || linux

package elasticsearch

import (
	"slices"
	"strings"
	"testing"
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
