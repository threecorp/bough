// Command bough-plugin-memory-mem0 is the official mem0 adapter
// the bough host spawns when `.bough.yaml` declares
// `memory_backends: [{ kind: mem0, role: external, ... }]`.
//
// The binary is intentionally tiny — the four-line `plugin.Serve`
// template every bough plugin uses. All real logic lives in
// plugins/memory/mem0/.
//
// Configuration is read from environment variables so the host
// can hand the plugin its endpoint / API key without a flag matrix.
// The host wraps this in a per-backend env block before spawning.
//
// Required:
//
//	BOUGH_MEMORY_MEM0_ENDPOINT   mem0 base URL (cloud or self-hosted)
//
// Optional:
//
//	BOUGH_MEMORY_MEM0_API_KEY    mem0 organisation API key
//	BOUGH_MEMORY_MEM0_NAMESPACE  multi-tenant prefix for every user_id
//	BOUGH_MEMORY_MEM0_TIMEOUT    Go duration (default "10s")
//
// See plugins/memory/mem0/CONTRACT.md for the full contract.
package main

import (
	"os"
	"time"

	"github.com/hashicorp/go-plugin"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
	"github.com/ikeikeikeike/bough/plugins/memory/mem0"
)

func main() {
	cfg := mem0.Config{
		Endpoint:  os.Getenv("BOUGH_MEMORY_MEM0_ENDPOINT"),
		APIKey:    os.Getenv("BOUGH_MEMORY_MEM0_API_KEY"),
		Namespace: os.Getenv("BOUGH_MEMORY_MEM0_NAMESPACE"),
	}
	if raw := os.Getenv("BOUGH_MEMORY_MEM0_TIMEOUT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			cfg.Timeout = d
		}
	}
	prov, err := mem0.New(cfg)
	if err != nil {
		_, _ = os.Stderr.WriteString("bough-plugin-memory-mem0: " + err.Error() + "\n")
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
