//go:build conformance

// The conformance test exercises the bough redis plugin end-to-end
// against a real redis container. Build tag `conformance` keeps
// docker out of the plain `go test ./...` path; CI invokes
// `go test -tags=conformance ./plugins/engine/redis/...` after a build
// of `bin/bough-plugin-redis`.
package redis_test

import (
	"os"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/conformance"
)

const (
	redisConformanceImage     = "redis:7"
	redisConformanceReadyMax  = 60 * time.Second
	redisConformancePluginEnv = "BOUGH_CONFORMANCE_PLUGIN_BIN"
)

// TestRedisConformance drives the bough contract against
// bin/bough-plugin-redis. NativeProbe is the stdlib-only
// conformance.RedisPing helper (RESP `PING` → `+PONG`), which is
// strictly stronger than AssertReachable: the TCP dial succeeds the
// moment redis-server is listening, but only an actual PING/PONG
// round-trip proves the protocol layer is up.
func TestRedisConformance(t *testing.T) {
	bin := os.Getenv(redisConformancePluginEnv)
	if bin == "" {
		t.Skipf("set %s to the bough-plugin-redis binary path", redisConformancePluginEnv)
	}
	conformance.Run(t, conformance.Config{
		PluginBinary:    bin,
		Image:           redisConformanceImage,
		ReadyTimeout:    redisConformanceReadyMax,
		IdempotentCount: 2,
		NativeProbe:     conformance.RedisPing,
		// The plugin only bind-mounts Datadir; redis-server writes
		// AOF to /data inside the container. chmod 0o000 on the host
		// would crash redis-server after Up returns, not before, so
		// the fault path cannot surface through Up's error.
		SkipDatadirPermission: true,
	})
}
