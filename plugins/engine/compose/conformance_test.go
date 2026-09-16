//go:build conformance

// Conformance harness for kind: compose. Unlike the four bundled
// plugins, this one does not key on Extras["docker.image"] — the
// image lives inside the wrapped compose file itself (testdata/
// compose.yml, a single redis:7-alpine service) — so Config.Image is
// left unset and the compose.* extras carry everything instead.
//
// compose.file is an ABSOLUTE path, not the "auba-api/compose.yml"
// relative-to-raw-worktree-root style production configs use: the
// conformance harness gives each phase an independent t.TempDir() as
// WorktreeRoot with no sibling-repo structure around it (see
// conformance/lifecycle.go), so filepath.Dir(WorktreeRoot) would
// never contain this fixture. Up() already supports absolute
// compose.file values for exactly this kind of caller.
//
// CI invokes this with:
//
//	go build -o dist/bough-plugin-compose ./cmd/bough-plugin-compose
//	BOUGH_CONFORMANCE_PLUGIN_BIN=$(pwd)/dist/bough-plugin-compose \
//	    go test -tags=conformance ./plugins/engine/compose/...
package compose_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/conformance"
)

func TestComposeConformance(t *testing.T) {
	bin := os.Getenv("BOUGH_CONFORMANCE_PLUGIN_BIN")
	if bin == "" {
		t.Skip("set BOUGH_CONFORMANCE_PLUGIN_BIN to the bough-plugin-compose binary")
	}
	fixture, err := filepath.Abs(filepath.Join("testdata", "compose.yml"))
	if err != nil {
		t.Fatalf("resolve fixture path: %v", err)
	}
	conformance.Run(t, conformance.Config{
		PluginBinary: bin,
		Extras: map[string]string{
			"compose.file":        fixture,
			"compose.service":     "redis",
			"compose.target_port": "6379",
		},
		ReadyTimeout:    60 * time.Second,
		IdempotentCount: 2,
		NativeProbe:     conformance.RedisPing,
		// Up() never reads extras["docker.image"] — the wrapped
		// service's image lives inside the compose file itself, so
		// forcing a bogus docker.image value here would not be
		// exercised either.
		SkipImagePullFailure: true,
	})
}
