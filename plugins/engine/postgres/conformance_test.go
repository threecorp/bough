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
// bin/bough-plugin-postgres. No NativeProbe — AssertReachable's
// TCP-level guard catches the v0.2.6-class "bridge IP advertised"
// bug, and a SELECT 1 round-trip requires a startup-message + auth
// handshake the suite would rather not embed. Plugin authors who
// want stronger probes can pass their own pgx-backed function.
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
		// Same reason as the mysql plugin: postgres only bind-mounts
		// Datadir, it never writes there itself, so chmod 0o000
		// cannot fault-inject through Up's return value.
		SkipDatadirPermission: true,
	})
}
