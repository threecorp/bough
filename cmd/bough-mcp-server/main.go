// Command bough-mcp-server exposes bough's memory subsystem to MCP
// clients (Claude Desktop, Cursor, etc.) over stdio JSON-RPC per the
// MCP 2025-11-25 spec.
//
// v0.6.0 is **read-only first** (round 4 AI #2):
//
//   - memory.query                Tool      (read-only)
//   - memory.store / .forget      refused with codeWriteForbidden
//   - bough://memory/scopes       Resource  (read-only)
//
// v0.6.x adds the state-changing surface behind an --allow-write
// CLI flag. The round 4 AI #1 zombie-process guard fires Graceful
// Shutdown the moment stdin closes so the MemoryBackend subprocess
// (= SQLite reference-fallback) never lingers and the DB file lock
// is released. See plugins/memory/sqlite/sqlite.go for the
// underlying lock semantics.
//
// Configuration is read from environment variables so an MCP client
// (= Claude Desktop's `mcpServers` block) can wire bough by setting
// the same env block it would for any other MCP server.
//
// Required:
//
//	(none — defaults pick up the sqlite reference-fallback)
//
// Optional:
//
//	BOUGH_MCP_SERVER_VERSION   reported in initialize response
//	                           (default: linker-set Version constant)
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/hashicorp/go-plugin"

	"github.com/ikeikeikeike/bough/internal/mcp"
	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// Version is reported in the MCP initialize handshake. Bumped per
// release; the v0.6.0 ship commit replaces the -dev suffix.
const Version = "v0.6.0-dev"

func main() {
	backend, kill, err := discoverSQLite()
	if err != nil {
		fmt.Fprintln(os.Stderr, "bough-mcp-server: "+err.Error())
		os.Exit(1)
	}

	version := Version
	if env := os.Getenv("BOUGH_MCP_SERVER_VERSION"); env != "" {
		version = env
	}

	server := mcp.NewServer(backend, kill, version)
	// Round 4 AI #1 zombie-process guard: stdin EOF triggers
	// Shutdown which kills the SQLite subprocess.
	mcp.WatchStdin(server)

	ctx := context.Background()
	if err := server.Run(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "bough-mcp-server: "+err.Error())
		server.Shutdown()
		os.Exit(1)
	}
	server.Shutdown()
}

// discoverSQLite spawns the bough-plugin-memory-sqlite binary as
// the v0.6 read-only MCP server's memory source. mem0 / Graphiti
// backends will land in v0.6.x once the CLI grows --backend.
func discoverSQLite() (memapi.MemoryBackend, func(), error) {
	binName := "bough-plugin-memory-sqlite"
	binPath, err := exec.LookPath(binName)
	if err != nil {
		return nil, nil, fmt.Errorf("%s not found on PATH: %w", binName, err)
	}
	cmd := exec.Command(binPath)
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  memapi.Handshake,
		Plugins:          memapi.PluginMap,
		Cmd:              cmd,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
	})
	rpc, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("gRPC dial: %w", err)
	}
	raw, err := rpc.Dispense(memapi.MemoryBackendPluginKey)
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("dispense: %w", err)
	}
	backend, ok := raw.(memapi.MemoryBackend)
	if !ok {
		client.Kill()
		return nil, nil, fmt.Errorf("plugin returned %T, not MemoryBackend", raw)
	}
	return backend, func() { client.Kill() }, nil
}
