//go:build conformance

// The conformance test exercises the bough postgres plugin end-to-end
// against a real postgres container. Build tag `conformance` keeps
// docker out of the plain `go test ./...` path; CI invokes
// `go test -tags=conformance ./plugins/engine/postgres/...` after a build
// of `bin/bough-plugin-postgres`.
package postgres_test

import (
	"os"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/conformance"
)

const (
	postgresConformanceImage     = "postgres:17"
	postgresConformanceReadyMax  = 120 * time.Second
	postgresConformancePluginEnv = "BOUGH_CONFORMANCE_PLUGIN_BIN"
)

// TestPostgresConformance drives the bough contract against
// bin/bough-plugin-postgres. NativeProbe is the stdlib-only
// conformance.PostgresProbe (an SSLRequest handshake requiring the
// server's 'S'/'N' reply): postgres sends no unsolicited greeting, so
// the SSLRequest — answerable before startup + auth — is the minimal
// round-trip that proves the protocol layer is up, strictly stronger
// than AssertReachable's TCP-only check.
func TestPostgresConformance(t *testing.T) {
	bin := os.Getenv(postgresConformancePluginEnv)
	if bin == "" {
		t.Skipf("set %s to the bough-plugin-postgres binary path", postgresConformancePluginEnv)
	}
	conformance.Run(t, conformance.Config{
		PluginBinary:    bin,
		Image:           postgresConformanceImage,
		ReadyTimeout:    postgresConformanceReadyMax,
		IdempotentCount: 2,
		NativeProbe:     conformance.PostgresProbe,
	})
}
