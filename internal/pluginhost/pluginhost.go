// Package pluginhost discovers a `bough-plugin-<kind>` binary on PATH,
// spawns it under Hashicorp go-plugin, and hands the caller a typed
// EngineProvider plus the kill func that tears the subprocess down.
//
// The host never imports a concrete plugin package — that would
// defeat the swap-by-binary contract bough chose over build-time
// linking — so this package is also the only place in the codebase
// that imports hashicorp/go-plugin on the host side.
//
// v0.4.0 fallback: the host first attempts the v0.4 handshake
// (BOUGH_ENGINE_PLUGIN / v2 / engine_provider) and, on failure, retries
// with the legacy v0.3 handshake (BOUGH_DB_PLUGIN / v1 / db_provider)
// so a v0.3.x plugin binary still spawns under a v0.4 host. The
// legacy path adapts the returned DBProvider into the new
// EngineProvider shape via legacyEngineAdapter. Removed in v0.5.0.
package pluginhost

import (
	"context"
	"fmt"
	"os/exec"

	engineapi "github.com/ikeikeikeike/bough/plugins/engine/api"
	dbapi "github.com/ikeikeikeike/bough/plugins/db/api"

	"github.com/hashicorp/go-plugin"
)

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
// The function attempts the v0.4 EngineProvider handshake first and,
// on protocol-version / magic-cookie mismatch, retries with the v0.3
// DBProvider handshake. The legacy result is wrapped in
// legacyEngineAdapter so callers always see EngineProvider regardless
// of which plugin version actually spawned.
func DiscoverFromBinary(binPath string) (engineapi.EngineProvider, func(), error) {
	if prov, cleanup, err := tryV04(binPath); err == nil {
		return prov, cleanup, nil
	}
	// The v0.4 attempt either rejected the handshake (BOUGH_ENGINE_PLUGIN
	// missing, ProtocolVersion=1) or the subprocess died before
	// dispensing. Either way, retry under the legacy handshake.
	return tryLegacyV03(binPath)
}

func tryV04(binPath string) (engineapi.EngineProvider, func(), error) {
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  engineapi.Handshake,
		Plugins:          engineapi.PluginMap,
		Cmd:              exec.Command(binPath),
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
	})
	rpc, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("pluginhost: %s gRPC dial (v0.4): %w", binPath, err)
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

func tryLegacyV03(binPath string) (engineapi.EngineProvider, func(), error) {
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  dbapi.Handshake,
		Plugins:          dbapi.PluginMap,
		Cmd:              exec.Command(binPath),
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
	})
	rpc, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("pluginhost: %s gRPC dial (v0.3 fallback): %w", binPath, err)
	}
	raw, err := rpc.Dispense(dbapi.DBProviderPluginKey)
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("pluginhost: %s dispense (v0.3) %q: %w", binPath, dbapi.DBProviderPluginKey, err)
	}
	legacy, ok := raw.(dbapi.DBProvider)
	if !ok {
		client.Kill()
		return nil, nil, fmt.Errorf("pluginhost: %s did not return a v0.3 DBProvider either (got %T)", binPath, raw)
	}
	return &legacyEngineAdapter{db: legacy}, func() { client.Kill() }, nil
}

// legacyEngineAdapter wraps a v0.3 DBProvider so the v0.4 host can
// drive it through the EngineProvider interface. Translation rules:
//
//   - ports[role:"main"|""].Port → DBProvider.Port int (single value)
//   - ports[*]                    → DBProvider sees only the main port
//   - initial_resources[type:"database"].Name → DBProvider.InitialDatabases (the rest dropped)
//   - PortRangeDefault             → {"main": [low, high]}
//
// Removed in v0.5.0.
type legacyEngineAdapter struct {
	db dbapi.DBProvider
}

func (a *legacyEngineAdapter) Up(ctx context.Context, req *engineapi.UpReq) error {
	return a.db.Up(ctx, dbapi.UpReq{
		Port:             engineapi.PickMainPort(req.Ports),
		Datadir:          req.Datadir,
		WorktreeRoot:     req.WorktreeRoot,
		SocketDir:        req.SocketDir,
		InitialDatabases: pickDatabaseNames(req.InitialResources),
		Extras:           req.Extras,
	})
}

func (a *legacyEngineAdapter) Down(ctx context.Context, req *engineapi.DownReq) error {
	port := 0
	if len(req.Ports) > 0 {
		port = req.Ports[0]
	}
	return a.db.Down(ctx, dbapi.DownReq{
		Port:               port,
		WorktreeRoot:       req.WorktreeRoot,
		GracefulTimeoutSec: req.GracefulTimeoutSec,
	})
}

func (a *legacyEngineAdapter) ReadyCheck(ctx context.Context, ports []int, timeoutSec int) (bool, error) {
	port := 0
	if len(ports) > 0 {
		port = ports[0]
	}
	return a.db.ReadyCheck(ctx, port, timeoutSec)
}

func (a *legacyEngineAdapter) Cleanup(ctx context.Context, datadir string, ports []int) error {
	port := 0
	if len(ports) > 0 {
		port = ports[0]
	}
	return a.db.Cleanup(ctx, datadir, port)
}

func (a *legacyEngineAdapter) PortRangeDefault(ctx context.Context) (map[string]engineapi.PortRange, error) {
	low, high, err := a.db.PortRangeDefault(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]engineapi.PortRange{"main": {Low: low, High: high}}, nil
}

func (a *legacyEngineAdapter) EnvVars(ctx context.Context, req *engineapi.EnvVarsReq) (map[string]string, error) {
	return a.db.EnvVars(ctx, dbapi.EnvVarsReq{
		Port:             engineapi.PickMainPort(req.Ports),
		InitialDatabases: pickDatabaseNames(req.InitialResources),
		SocketDir:        req.SocketDir,
	})
}

func pickDatabaseNames(rs []engineapi.ResourceSpec) []string {
	var out []string
	for _, r := range rs {
		if r.Type == "database" || r.Type == "" {
			out = append(out, r.Name)
		}
	}
	return out
}
