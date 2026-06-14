// Package pluginhost discovers a `bough-plugin-<kind>` binary on PATH,
// spawns it under Hashicorp go-plugin, and hands the caller a typed
// DBProvider plus the kill func that tears the subprocess down.
//
// The host never imports a concrete plugin package — that would defeat
// the swap-by-binary contract bough chose over build-time linking — so
// this package is also the only place in the codebase that imports
// hashicorp/go-plugin on the host side.
package pluginhost

import (
	"fmt"
	"os/exec"

	api "github.com/ikeikeikeike/bough/plugins/db/api"
	"github.com/hashicorp/go-plugin"
)

// Discover finds the binary `bough-plugin-<kind>` on PATH, executes it
// under go-plugin's gRPC protocol, and returns a typed DBProvider
// client. The caller MUST invoke the returned cleanup func (typically
// via defer) when finished — go-plugin keeps the subprocess alive
// otherwise.
//
// "Not found on PATH" surfaces as a plain error so the host CLI can
// emit a configuration-friendly message ("install bough-plugin-mysql
// or set kind to a plugin you have"). All other failures (handshake
// rejected, dispense error, type assertion failure) abort with the
// underlying error chained.
func Discover(kind string) (api.DBProvider, func(), error) {
	binName := fmt.Sprintf("bough-plugin-%s", kind)
	binPath, err := exec.LookPath(binName)
	if err != nil {
		return nil, nil, fmt.Errorf("pluginhost: %s not found on PATH: %w", binName, err)
	}
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  api.Handshake,
		Plugins:          api.PluginMap,
		Cmd:              exec.Command(binPath),
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
	})
	rpc, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("pluginhost: %s gRPC dial: %w", binName, err)
	}
	raw, err := rpc.Dispense(api.DBProviderPluginKey)
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("pluginhost: %s dispense %q: %w", binName, api.DBProviderPluginKey, err)
	}
	prov, ok := raw.(api.DBProvider)
	if !ok {
		client.Kill()
		return nil, nil, fmt.Errorf("pluginhost: %s did not return a DBProvider (got %T)", binName, raw)
	}
	return prov, func() { client.Kill() }, nil
}
