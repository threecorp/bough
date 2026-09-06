//go:build darwin || linux

// Package mysql implements the bough EngineProvider for MySQL. The
// plugin binary spawned from cmd/bough-plugin-mysql/main.go wraps this
// Provider as a Hashicorp go-plugin gRPC server.
//
// Up, ReadyCheck and Down are the runtime-dependent third of the
// contract and are delegated to an api.Backend; docker.go holds the
// only one bundled today, and New() registers it. Cleanup, EnvVars and
// PortRangeDefault never depended on where the engine runs and stay
// here.
//
// darwin / linux only — the docker helpers this plugin shares with its
// siblings carry the same build tag.
package mysql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"
)

// Provider implements api.EngineProvider for MySQL. Construct via New()
// so any future tunables can be threaded as struct fields without
// breaking the constructor surface.
type Provider struct {
	// PortLow / PortHigh override PortRangeDefault. Production callers
	// leave them zero and the defaults (42000, 44999) take effect.
	PortLow  int
	PortHigh int

	// Backends is the extras["backend"] table New() seeds. A second
	// runtime is one more entry here plus its api.Backend.
	Backends api.Backends
}

// New returns a Provider with production defaults.
func New() *Provider {
	return &Provider{Backends: api.Backends{api.DefaultBackend: dockerBackend{}}}
}

const (
	defaultPortLow  = 42000
	defaultPortHigh = 44999
)

// Up starts the engine on the backend extras["backend"] names, or on
// api.DefaultBackend when the host sends none.
func (p *Provider) Up(ctx context.Context, req *api.UpReq) error {
	backend, err := p.Backends.ForUp(req.Extras)
	if err != nil {
		return fmt.Errorf("mysql: Up: %w", err)
	}
	return backend.Up(ctx, req)
}

// ReadyCheck polls for mysql connectivity on the main port for up to
// `timeoutSec` seconds.
//
// The single-port mysql plugin reads ports[0] (the host orders entries
// to match the PortRangeDefault keys; "main" is always first).
func (p *Provider) ReadyCheck(ctx context.Context, ports []int, timeoutSec int) (bool, error) {
	port := firstListenPort(ports)
	if port <= 0 {
		return false, fmt.Errorf("mysql: ReadyCheck: invalid ports %v", ports)
	}
	backend, err := p.Backends.ForPort(ctx, port)
	if err != nil {
		return false, fmt.Errorf("mysql: ReadyCheck: %w", err)
	}
	return backend.ReadyCheck(ctx, port, timeoutSec)
}

// Down stops the engine holding the main port. DownReq carries no
// backend token, so the backend is resolved from what is running.
func (p *Provider) Down(ctx context.Context, req *api.DownReq) error {
	backend, err := p.Backends.ForPort(ctx, firstListenPort(req.Ports))
	if err != nil {
		return fmt.Errorf("mysql: Down: %w", err)
	}
	return backend.Down(ctx, req)
}

// Cleanup removes the mysqld datadir. Down must have already converged
// on "nothing listening on Port"; calling Cleanup with mysqld still
// alive would delete the datadir under an open mysqld and crash it.
func (p *Provider) Cleanup(_ context.Context, datadir string, _ []int) error {
	if datadir == "" {
		return errors.New("mysql: Cleanup: datadir is required")
	}
	return os.RemoveAll(datadir)
}

// PortRangeDefault returns the plugin's recommended port range under
// role "main" (the only role this single-port engine uses). Used by
// the host's `bough plugins list` and as the YAML default when
// `engines[].port_ranges` is empty.
func (p *Provider) PortRangeDefault(_ context.Context) (map[string]api.PortRange, error) {
	low := p.PortLow
	high := p.PortHigh
	if low == 0 {
		low = defaultPortLow
	}
	if high == 0 {
		high = defaultPortHigh
	}
	return map[string]api.PortRange{"main": {Low: low, High: high}}, nil
}

// EnvVars exposes the per-worktree connection coordinates to the host
// so the host can render BOUGH_MYSQL_* into every .env.local that
// declares them in its YAML template. The container publishes TCP
// only, so no socket path is advertised.
func (p *Provider) EnvVars(_ context.Context, req *api.EnvVarsReq) (map[string]string, error) {
	port := api.PickMainPort(req.Ports)
	return map[string]string{
		"BOUGH_MYSQL_HOST": "127.0.0.1",
		"BOUGH_MYSQL_PORT": strconv.Itoa(port),
	}, nil
}

// firstListenPort returns ports[0] (the host orders entries so the
// main role is first for single-port engines), or 0 when ports is
// empty. Kept as a tiny helper so the call sites read uniformly.
func firstListenPort(ports []int) int {
	if len(ports) > 0 {
		return ports[0]
	}
	return 0
}
