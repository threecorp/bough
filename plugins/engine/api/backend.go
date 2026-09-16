package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// DefaultBackend is the extras["backend"] token Up assumes when the host
// sends none; every bundled plugin registers docker under it.
const DefaultBackend = "docker"

// Backend is the runtime-dependent part of EngineProvider: where the
// engine process lives decides how it is started, probed and stopped.
// Cleanup, EnvVars and PortRangeDefault never depended on it and stay on
// the Provider.
type Backend interface {
	Up(ctx context.Context, req *UpReq) error
	ReadyCheck(ctx context.Context, port, timeoutSec int) (bool, error)
	Down(ctx context.Context, req *DownReq) error
	// Running reports whether this backend owns a live engine on port.
	// Consulted only when more than one backend is registered: Down and
	// ReadyCheck carry no token on the wire, and a stopped leftover must
	// not count (CONTRACT clause 6).
	Running(ctx context.Context, port int) bool
}

// Backends is the token → implementation table a plugin's New() seeds.
// A second runtime is one more entry here plus its Backend.
type Backends map[string]Backend

// UnknownBackendError is ForUp's refusal. The registered tokens ride
// along because naming them is the fix the operator needs.
type UnknownBackendError struct {
	Name       string
	Registered []string
}

func (e *UnknownBackendError) Error() string {
	return fmt.Sprintf("unknown backend %q (this plugin provides %s)", e.Name, strings.Join(e.Registered, ", "))
}

var errNoBackends = errors.New("no backends registered; construct the Provider via New()")

// ForUp resolves the backend Up runs on. An empty token means
// DefaultBackend; an unregistered one is refused here rather than
// silently running whatever is registered.
func (b Backends) ForUp(extras map[string]string) (Backend, error) {
	if len(b) == 0 {
		return nil, errNoBackends
	}
	name := extras["backend"]
	if name == "" {
		name = DefaultBackend
	}
	if impl, ok := b[name]; ok {
		return impl, nil
	}
	return nil, &UnknownBackendError{Name: name, Registered: b.names()}
}

// ForPort resolves the backend for a Down or ReadyCheck. A single
// registered backend is returned without probing; otherwise the first
// by name whose Running is true wins, falling back to DefaultBackend —
// Down is idempotent everywhere, so a fallback when nothing runs stops
// nothing.
func (b Backends) ForPort(ctx context.Context, port int) (Backend, error) {
	switch len(b) {
	case 0:
		return nil, errNoBackends
	case 1:
		for _, impl := range b {
			return impl, nil
		}
	}
	for _, name := range b.names() {
		if b[name].Running(ctx, port) {
			return b[name], nil
		}
	}
	if impl, ok := b[DefaultBackend]; ok {
		return impl, nil
	}
	return b[b.names()[0]], nil
}

func (b Backends) names() []string {
	return slices.Sorted(maps.Keys(b))
}
