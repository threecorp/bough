//go:build conformance

package sqlite_test

import (
	"os"
	"testing"

	memconf "github.com/ikeikeikeike/bough/conformance/memory"
)

// TestConformance pins the bough SQLite reference-fallback against
// the conformance/memory contract. CI passes the binary path
// through BOUGH_CONFORMANCE_MEMORY_PLUGIN_BIN; locally:
//
//	go build -o dist/bough-plugin-memory-sqlite ./cmd/bough-plugin-memory-sqlite
//	BOUGH_CONFORMANCE_MEMORY_PLUGIN_BIN=$PWD/dist/bough-plugin-memory-sqlite \
//	    go test -tags=conformance ./plugins/memory/sqlite/...
func TestConformance(t *testing.T) {
	memconf.Run(t, memconf.Config{
		PluginBinary: os.Getenv("BOUGH_CONFORMANCE_MEMORY_PLUGIN_BIN"),
		Datadir:      t.TempDir(),
	})
}
