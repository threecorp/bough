// Package capability is the host-side CapabilityCompiler
// orchestrator (Ν-1.4). It walks an approved set of instincts,
// synthesises CapabilityArtifacts per target kind + format, and
// dispatches each artifact through the Emitter registry.
//
// Plugin authors NEVER import this package — their surface is
// plugins/capability/api/. Internals here mirror the
// internal/instinct/coordinator.go shape so the two host
// orchestrators stay analogous.
package capability

import (
	"fmt"
	"sort"
	"sync"

	capapi "github.com/ikeikeikeike/bough/plugins/capability/api"
)

// Registry keeps the Format → Emitter mapping the compiler dispatches
// against. Round 4 priority A13: v0.6 ships a Go map; v0.6.x can
// migrate to gRPC plugin slots without changing the public surface
// because Emitter lives in plugins/capability/api/.
type Registry struct {
	mu   sync.RWMutex
	rows map[string]capapi.Emitter
}

// NewRegistry constructs an empty registry. The CLI bootstrap is
// responsible for registering the three default emitters
// (agent-skill / claude-skill / mcp-*) before handing the registry
// to a Compiler.
func NewRegistry() *Registry {
	return &Registry{rows: make(map[string]capapi.Emitter)}
}

// Register adds or replaces an emitter. The format string is
// canonical and lowercase by convention; a second Register with
// the same format silently overwrites the existing entry so a
// downstream extension can swap a built-in.
func (r *Registry) Register(e capapi.Emitter) {
	if e == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[e.Format()] = e
}

// Lookup returns the emitter for a format, with a stable error
// when the caller asked for something the registry doesn't know.
// The error includes the known set so `bough capability compile
// --to <unknown>` produces actionable help.
func (r *Registry) Lookup(format string) (capapi.Emitter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.rows[format]
	if !ok {
		return nil, fmt.Errorf("capability emitter %q not registered (known: %v)", format, r.knownFormatsLocked())
	}
	return e, nil
}

// Formats returns the sorted list of registered formats. Used by
// the CLI for `bough capability compile --list-targets`.
func (r *Registry) Formats() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.knownFormatsLocked()
}

func (r *Registry) knownFormatsLocked() []string {
	out := make([]string, 0, len(r.rows))
	for k := range r.rows {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
