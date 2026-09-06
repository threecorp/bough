//go:build darwin || linux

// Package elasticsearch implements the bough EngineProvider for
// Elasticsearch. The plugin binary spawned from
// cmd/bough-plugin-elasticsearch/main.go wraps this Provider as a
// Hashicorp go-plugin gRPC server.
//
// Up, ReadyCheck and Down are the runtime-dependent third of the
// contract and are delegated to an api.Backend; docker.go holds the
// only one bundled today, and New() registers it. Cleanup, EnvVars and
// PortRangeDefault never depended on where the engine runs and stay
// here.
//
// darwin / linux only — the docker helpers this plugin shares with its
// siblings carry the same build tag.
package elasticsearch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"
)

// Provider implements api.EngineProvider for Elasticsearch. Construct
// via New() so any future tunables can be threaded as struct fields
// without breaking the constructor surface.
type Provider struct {
	// PortLow / PortHigh override PortRangeDefault. Production callers
	// leave them zero and the defaults (56000, 58999) take effect.
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
	defaultPortLow  = 56000
	defaultPortHigh = 58999
)

// heapSizePattern matches a JVM heap size like "512m", "1g", "2048k"
// (an integer with an optional k/m/g suffix, case-insensitive).
var heapSizePattern = regexp.MustCompile(`^\d+[kmgKMG]?$`)

// validateHeap rejects a heap value that cannot safely reach the JVM.
// The value rides into the container as ES_JAVA_OPTS, where a malformed
// one would otherwise surface late as an opaque JVM / container startup
// failure instead of bough's own actionable error.
func validateHeap(heap string) error {
	if !heapSizePattern.MatchString(heap) {
		return fmt.Errorf("invalid heap %q from extras (want e.g. 512m, 1g)", heap)
	}
	return nil
}

// Up starts the engine on the backend extras["backend"] names, or on
// api.DefaultBackend when the host sends none.
func (p *Provider) Up(ctx context.Context, req *api.UpReq) error {
	backend, err := p.Backends.ForUp(req.Extras)
	if err != nil {
		return fmt.Errorf("elasticsearch: Up: %w", err)
	}
	return backend.Up(ctx, req)
}

// ReadyCheck polls for Elasticsearch on the main port for up to
// `timeoutSec` seconds.
func (p *Provider) ReadyCheck(ctx context.Context, ports []int, timeoutSec int) (bool, error) {
	port := firstListenPort(ports)
	if port <= 0 {
		return false, fmt.Errorf("elasticsearch: ReadyCheck: invalid ports %v", ports)
	}
	backend, err := p.Backends.ForPort(ctx, port)
	if err != nil {
		return false, fmt.Errorf("elasticsearch: ReadyCheck: %w", err)
	}
	return backend.ReadyCheck(ctx, port, timeoutSec)
}

// Down stops the engine holding the main port. DownReq carries no
// backend token, so the backend is resolved from what is running.
func (p *Provider) Down(ctx context.Context, req *api.DownReq) error {
	backend, err := p.Backends.ForPort(ctx, firstListenPort(req.Ports))
	if err != nil {
		return fmt.Errorf("elasticsearch: Down: %w", err)
	}
	return backend.Down(ctx, req)
}

// Cleanup removes the elasticsearch datadir.
func (p *Provider) Cleanup(_ context.Context, datadir string, _ []int) error {
	if datadir == "" {
		return errors.New("elasticsearch: Cleanup: datadir is required")
	}
	return os.RemoveAll(datadir)
}

// PortRangeDefault returns the plugin's recommended port range under
// role "main" (the only role this single-port engine uses).
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

// EnvVars exposes the per-worktree connection coordinates. The URL is
// the http://… form most Elasticsearch SDKs (go-elasticsearch,
// elasticsearch-py, @elastic/elasticsearch) accept verbatim.
func (p *Provider) EnvVars(_ context.Context, req *api.EnvVarsReq) (map[string]string, error) {
	port := api.PickMainPort(req.Ports)
	return map[string]string{
		"BOUGH_ELASTICSEARCH_HOST": "127.0.0.1",
		"BOUGH_ELASTICSEARCH_PORT": strconv.Itoa(port),
		"BOUGH_ELASTICSEARCH_URL":  fmt.Sprintf("http://127.0.0.1:%d", port),
	}, nil
}

// firstListenPort returns ports[0], or 0 when ports is empty.
func firstListenPort(ports []int) int {
	if len(ports) > 0 {
		return ports[0]
	}
	return 0
}
