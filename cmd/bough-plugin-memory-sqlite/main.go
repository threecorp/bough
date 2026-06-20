// Command bough-plugin-memory-sqlite is the SQLite reference-fallback
// memory backend the bough host spawns when `.bough.yaml` declares
// `memory_backends: [{ kind: sqlite, role: reference-fallback }]`.
//
// The binary is intentionally tiny (the four-line `plugin.Serve`
// template every bough plugin uses). All the actual logic lives in
// plugins/memory/sqlite/.
//
// Path resolution: the SQLite file path is supplied at Up time by
// the host through `.bough.yaml`'s memory_backends[*].path. The
// plugin opens the file lazily on the first RPC so an inconsistent
// path is surfaced as a normal Store / Query error rather than a
// startup panic.
//
// For local debugging set BOUGH_MEMORY_SQLITE_PATH=/path/to/db.sqlite
// in the environment before running this binary directly through
// `go run`. The conformance suite uses this env var to point at a
// per-test TempDir.
package main

import (
	"os"
	"path/filepath"

	"github.com/hashicorp/go-plugin"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
	"github.com/ikeikeikeike/bough/plugins/memory/sqlite"
)

func main() {
	path := os.Getenv("BOUGH_MEMORY_SQLITE_PATH")
	if path == "" {
		path = filepath.Join(os.TempDir(), "bough-memory-sqlite.db")
	}
	// Ensure parent directory exists — sqlite.Open does not create
	// intermediate directories, and the host typically configures a
	// path under `.bough/memory/` that may not exist yet on a fresh
	// monorepo.
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	prov, err := sqlite.New(path)
	if err != nil {
		// Plugin protocol requires a working Impl at registration.
		// If we cannot open the DB, surface the failure as an
		// immediate exit so the host's Discover sees a clear error.
		_, _ = os.Stderr.WriteString("bough-plugin-memory-sqlite: cannot open DB: " + err.Error() + "\n")
		os.Exit(1)
	}
	defer func() { _ = prov.Close() }()

	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: memapi.Handshake,
		Plugins: map[string]plugin.Plugin{
			memapi.MemoryBackendPluginKey: &memapi.MemoryBackendPlugin{Impl: prov},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}
