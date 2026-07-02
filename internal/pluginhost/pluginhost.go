// Package pluginhost discovers a `bough-plugin-<kind>` binary on PATH,
// spawns it under Hashicorp go-plugin, and hands the caller a typed
// EngineProvider plus the kill func that tears the subprocess down.
//
// The host never imports a concrete plugin package — that would
// defeat the swap-by-binary contract bough chose over build-time
// linking — so this package is also the only place in the codebase
// that imports hashicorp/go-plugin on the host side.
//
// v0.5.0 removed the v0.3 DBProvider fallback handshake that v0.4.x
// had carried for transitional use. Users running a v0.3.x plugin
// binary must upgrade to a v0.4-or-newer plugin (the v0.3 series has
// been End-Of-Lifed for three release cycles by the time v0.5 ships).
// The removal is what frees pluginhost to add the four new plugin
// kinds in v0.5 (memory / instinct / capability / evaluator) without
// the legacy retry path interfering with their handshakes.
package pluginhost

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"

	engineapi "github.com/ikeikeikeike/bough/plugins/engine/api"
)

// guard against the import being optimised out if we ever reduce the
// engine plugin surface; context.TODO is the cheapest reference.
var _ = context.TODO

// Discover finds the binary `bough-plugin-<kind>` on PATH, executes
// it under go-plugin's gRPC protocol, and returns a typed
// EngineProvider client. The caller MUST invoke the returned cleanup
// func (typically via defer) when finished — go-plugin keeps the
// subprocess alive otherwise.
//
// "Not found on PATH" surfaces as a plain error so the host CLI can
// emit a configuration-friendly message ("install bough-plugin-mysql
// or set kind to a plugin you have"). All other failures (handshake
// rejected, dispense error, type assertion failure) abort with the
// underlying error chained.
func Discover(kind string) (engineapi.EngineProvider, func(), error) {
	binName := fmt.Sprintf("bough-plugin-%s", kind)
	binPath, err := exec.LookPath(binName)
	if err != nil {
		return nil, nil, fmt.Errorf("pluginhost: %s not found on PATH: %w", binName, err)
	}
	return DiscoverFromBinary(binPath)
}

// DiscoverFromBinary spawns the plugin binary at `binPath` under
// go-plugin's gRPC protocol and returns a typed EngineProvider
// client. Same contract as Discover but skips PATH lookup, so the
// caller can pin an exact binary (used by the conformance suite and
// the release pipeline's acceptance tests).
//
// v0.5.0 simplification: only the v0.4 EngineProvider handshake is
// attempted. v0.3 binaries report `error: BOUGH_ENGINE_PLUGIN cookie
// missing` and the caller surfaces that to the user with the upgrade
// hint baked into the package docstring.
func DiscoverFromBinary(binPath string) (engineapi.EngineProvider, func(), error) {
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  engineapi.Handshake,
		Plugins:          engineapi.PluginMap,
		Cmd:              exec.Command(binPath),
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		Logger:           pluginLogger(),
	})
	rpc, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("pluginhost: %s gRPC dial: %w (hint: v0.3 plugins are no longer supported in v0.5+; rebuild against plugins/engine/api)", binPath, err)
	}
	raw, err := rpc.Dispense(engineapi.EngineProviderPluginKey)
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("pluginhost: %s dispense %q: %w", binPath, engineapi.EngineProviderPluginKey, err)
	}
	prov, ok := raw.(engineapi.EngineProvider)
	if !ok {
		client.Kill()
		return nil, nil, fmt.Errorf("pluginhost: %s did not return an EngineProvider (got %T)", binPath, raw)
	}
	return prov, func() { client.Kill() }, nil
}

// pluginLogger builds the hclog logger go-plugin uses for the spawned
// plugin subprocess. go-plugin's default managed logger is Trace, which
// floods `bough create` stderr with "waiting for RPC address" / "plugin
// address" DEBUG/TRACE lines that bury bough's own [bough] progress
// lines and make a slow step look hung. Default Warn keeps the output
// clean; set BOUGH_PLUGIN_LOG=trace|debug|info|warn|error to restore the
// verbose go-plugin logs when debugging a plugin handshake.
func pluginLogger() hclog.Logger {
	return hclog.New(&hclog.LoggerOptions{
		Name:   "plugin",
		Output: os.Stderr,
		Level:  pluginLogLevel(),
	})
}

func pluginLogLevel() hclog.Level {
	if lvl := hclog.LevelFromString(os.Getenv("BOUGH_PLUGIN_LOG")); lvl != hclog.NoLevel {
		return lvl
	}
	return hclog.Warn
}
