//go:build conformance

// The conformance test exercises the bough elasticsearch plugin
// end-to-end against a real ES container. Build tag `conformance`
// keeps docker out of the plain `go test ./...` path; CI invokes
// `go test -tags=conformance ./plugins/engine/elasticsearch/...` after a
// build of `bin/bough-plugin-elasticsearch`.
//
// The v0.2.6 regression that motivated the suite belongs to this
// plugin specifically: sniffing clients (olivere/elastic et al.)
// dial whatever `_nodes/http` advertises, and the container's
// internal bridge IP is unreachable from the host. AssertReachable
// catches that class deterministically; ElasticsearchGetRoot adds a
// HTTP 200 round-trip on the bough-allocated host port for good
// measure.
package elasticsearch_test

import (
	"os"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/conformance"
)

const (
	elasticsearchConformanceImage     = "docker.elastic.co/elasticsearch/elasticsearch:9.5.3"
	elasticsearchConformanceReadyMax  = 300 * time.Second // JVM warmup on cold machines
	elasticsearchConformancePluginEnv = "BOUGH_CONFORMANCE_PLUGIN_BIN"
	// Set to run the suite against another line (7.17.29 is the one this
	// plugin shipped against before 9.x became the default).
	elasticsearchConformanceImageEnv = "BOUGH_CONFORMANCE_ES_IMAGE"
)

// TestElasticsearchConformance drives the bough contract against
// bin/bough-plugin-elasticsearch.
func TestElasticsearchConformance(t *testing.T) {
	bin := os.Getenv(elasticsearchConformancePluginEnv)
	if bin == "" {
		t.Skipf("set %s to the bough-plugin-elasticsearch binary path", elasticsearchConformancePluginEnv)
	}
	image := elasticsearchConformanceImage
	if v := os.Getenv(elasticsearchConformanceImageEnv); v != "" {
		image = v
	}
	conformance.Run(t, conformance.Config{
		PluginBinary:    bin,
		Image:           image,
		ReadyTimeout:    elasticsearchConformanceReadyMax,
		IdempotentCount: 2,
		NativeProbe:     conformance.ElasticsearchGetRoot,
		// SkipDatadirPermission is intentionally NOT set: the
		// Fault_DatadirPermission case forces the host-process
		// (process-compose) backend, whose Up mkdirs Datadir
		// synchronously and so surfaces a 0o000 parent as a real Up
		// error. See conformance.Config.DatadirFaultBackend.
	})
}
