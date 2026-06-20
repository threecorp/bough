// Package memory is the MemoryBackend plugin contract test suite.
// Plugin authors verify their implementation by adding one file:
//
//	//go:build conformance
//	package myplugin_test
//
//	import (
//	    "os"
//	    "testing"
//	    memconf "github.com/ikeikeikeike/bough/conformance/memory"
//	)
//
//	func TestPluginConformance(t *testing.T) {
//	    memconf.Run(t, memconf.Config{
//	        PluginBinary: os.Getenv("BOUGH_CONFORMANCE_MEMORY_PLUGIN_BIN"),
//	        Datadir:      t.TempDir(),
//	    })
//	}
//
// The suite spawns the plugin under hashicorp/go-plugin (the same
// path the bough host uses in production), exercises the seven RPCs,
// and asserts the dedupe / budget / concurrency invariants the round
// 3 external review insisted v0.5 backends must honour.
//
// Memory plugins differ from engine plugins in that they need no
// container, no port allocation, and no readiness probe (Health is
// the only liveness check). The suite consequently runs without
// Docker on macOS and Linux runners alike.
package memory

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/go-plugin"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// Config drives a conformance run. Only PluginBinary is required;
// every other field has a default chosen to match the SQLite
// reference-fallback's tuning.
type Config struct {
	// PluginBinary is the absolute path to the go-plugin server
	// binary the suite will spawn.
	PluginBinary string

	// Datadir is the on-disk root the suite will pass into Store /
	// Query / Forget. Plugin authors pass t.TempDir().
	Datadir string

	// MaxResults / MaxTokens shadow the host's retrieve hard limits.
	// Default: 12 / 4000 (round 3 AI #1 fallbacks).
	MaxResults int
	MaxTokens  int

	// CallTimeout bounds each RPC call. Default: 10s — enough for
	// SQLite WAL contention to settle but not so much that a stuck
	// backend hangs CI indefinitely.
	CallTimeout time.Duration
}

func (c *Config) applyDefaults() {
	if c.MaxResults == 0 {
		c.MaxResults = 12
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 4000
	}
	if c.CallTimeout == 0 {
		c.CallTimeout = 10 * time.Second
	}
}

// Run executes the full conformance suite against the plugin
// binary at cfg.PluginBinary. The suite skips if PluginBinary is
// empty so CI cells that did not provision a binary do not fail.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.PluginBinary == "" {
		t.Skip("memory conformance: BOUGH_CONFORMANCE_MEMORY_PLUGIN_BIN unset; skipping")
	}
	cfg.applyDefaults()

	t.Run("Lifecycle", func(t *testing.T) {
		client, cleanup := spawn(t, cfg.PluginBinary, perTestDB(t, cfg, "lifecycle"))
		defer cleanup()
		runLifecycle(t, client, cfg)
	})

	t.Run("Bloat", func(t *testing.T) {
		client, cleanup := spawn(t, cfg.PluginBinary, perTestDB(t, cfg, "bloat"))
		defer cleanup()
		runBloat(t, client, cfg)
	})

	t.Run("Concurrency", func(t *testing.T) {
		client, cleanup := spawn(t, cfg.PluginBinary, perTestDB(t, cfg, "concurrency"))
		defer cleanup()
		runConcurrency(t, client, cfg)
	})
}

// perTestDB allocates a unique SQLite-style DB path inside cfg.Datadir
// per sub-test so the conformance suite never sees state bled in from
// the previous sub-test or the previous test session. Memory plugins
// read the path through BOUGH_MEMORY_SQLITE_PATH; other backend kinds
// either ignore the variable (mem0 talks to a remote) or document a
// similar convention in their plugin author guide.
func perTestDB(t *testing.T, cfg Config, label string) string {
	t.Helper()
	if cfg.Datadir == "" {
		return ""
	}
	return filepath.Join(cfg.Datadir, fmt.Sprintf("%s.db", label))
}

// spawn launches the plugin binary under go-plugin and returns a
// MemoryBackend client plus the cleanup func that kills the
// subprocess. The suite's t.Helper() promise keeps the line numbers
// on failure pointing at the call site, not into spawn.
//
// dbPath, when non-empty, is exported into the plugin process as
// BOUGH_MEMORY_SQLITE_PATH so the SQLite reference-fallback (and
// any community plugin that respects the env convention) gets its
// per-sub-test datadir. Backends that ignore the variable
// (e.g. mem0 talking to a remote) just see an unused env entry.
func spawn(t *testing.T, binPath string, dbPath string) (memapi.MemoryBackend, func()) {
	t.Helper()
	cmd := exec.Command(binPath)
	if dbPath != "" {
		cmd.Env = append(os.Environ(), "BOUGH_MEMORY_SQLITE_PATH="+dbPath)
	}
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  memapi.Handshake,
		Plugins:          memapi.PluginMap,
		Cmd:              cmd,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
	})
	rpc, err := client.Client()
	if err != nil {
		client.Kill()
		t.Fatalf("gRPC dial %q: %v", binPath, err)
	}
	raw, err := rpc.Dispense(memapi.MemoryBackendPluginKey)
	if err != nil {
		client.Kill()
		t.Fatalf("dispense %q: %v", memapi.MemoryBackendPluginKey, err)
	}
	backend, ok := raw.(memapi.MemoryBackend)
	if !ok {
		client.Kill()
		t.Fatalf("plugin returned %T, not MemoryBackend", raw)
	}
	return backend, func() { client.Kill() }
}

// ctx returns a timeout-bound context for a single RPC. Calling
// code defers cancel().
func (c *Config) ctx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), c.CallTimeout)
}
