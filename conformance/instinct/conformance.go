// Package instinct is the InstinctMinter plugin contract test suite.
// Plugin authors verify their implementation by adding one file:
//
//	//go:build conformance
//	package myplugin_test
//
//	import (
//	    "os"
//	    "testing"
//	    instconf "github.com/ikeikeikeike/bough/conformance/instinct"
//	)
//
//	func TestPluginConformance(t *testing.T) {
//	    instconf.Run(t, instconf.Config{
//	        PluginBinary: os.Getenv("BOUGH_CONFORMANCE_INSTINCT_PLUGIN_BIN"),
//	    })
//	}
//
// The suite is intentionally small: InstinctMinter has one RPC
// (Mint), so the conformance contract is mostly "given these traces,
// the candidates you return must satisfy these invariants". bough
// ships an in-process default minter (`internal/instinct.builtin_minter`)
// so this contract is exercised in practice only by v0.6+ LLM-backed
// plugins (SkillX, Anything2Skill).
package instinct

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/hashicorp/go-plugin"

	instapi "github.com/ikeikeikeike/bough/plugins/instinct/api"
)

// Config drives a conformance run. PluginBinary is required.
type Config struct {
	PluginBinary string
	CallTimeout  time.Duration
}

func (c *Config) applyDefaults() {
	if c.CallTimeout == 0 {
		c.CallTimeout = 10 * time.Second
	}
}

// Run executes the full conformance suite against the plugin
// binary at cfg.PluginBinary.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.PluginBinary == "" {
		t.Skip("instinct conformance: BOUGH_CONFORMANCE_INSTINCT_PLUGIN_BIN unset; skipping")
	}
	cfg.applyDefaults()

	t.Run("Lifecycle", func(t *testing.T) {
		client, cleanup := spawn(t, cfg.PluginBinary)
		defer cleanup()
		runLifecycle(t, client, cfg)
	})
}

func spawn(t *testing.T, binPath string) (instapi.InstinctMinter, func()) {
	t.Helper()
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  instapi.Handshake,
		Plugins:          instapi.PluginMap,
		Cmd:              exec.Command(binPath),
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
	})
	rpc, err := client.Client()
	if err != nil {
		client.Kill()
		t.Fatalf("gRPC dial %q: %v", binPath, err)
	}
	raw, err := rpc.Dispense(instapi.InstinctMinterPluginKey)
	if err != nil {
		client.Kill()
		t.Fatalf("dispense %q: %v", instapi.InstinctMinterPluginKey, err)
	}
	minter, ok := raw.(instapi.InstinctMinter)
	if !ok {
		client.Kill()
		t.Fatalf("plugin returned %T, not InstinctMinter", raw)
	}
	return minter, func() { client.Kill() }
}

func (c *Config) ctx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), c.CallTimeout)
}
