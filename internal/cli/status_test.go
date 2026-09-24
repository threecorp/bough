package cli

import (
	"testing"

	"github.com/ikeikeikeike/bough/internal/config"
	engineapi "github.com/ikeikeikeike/bough/plugins/engine/api"
)

// TestComputeEngineBackends_ExplicitBackendFieldWins pins the base
// case: an explicit `backend:` YAML value is reported verbatim.
func TestComputeEngineBackends_ExplicitBackendFieldWins(t *testing.T) {
	cfg := &config.Config{Engines: []config.Engine{
		{Kind: "mysql", Backend: "docker"},
	}}
	got := computeEngineBackends(cfg, map[string]bool{"mysql": true})
	if got["mysql"] != "docker" {
		t.Errorf("Backend[mysql] = %q, want %q", got["mysql"], "docker")
	}
}

// TestComputeEngineBackends_ExtrasBackendOverrideIsHonored is the
// regression guard for the wave-3 review finding: computeEngineBackends
// only checked eng.Backend, never eng.Extras["backend"] — the override
// path create.go's buildEngineExtras treats as equally authoritative
// (eng.Backend > extras["backend"] > default). An engine pinned via
// extras.backend used to be silently treated as unset by status.
func TestComputeEngineBackends_ExtrasBackendOverrideIsHonored(t *testing.T) {
	cfg := &config.Config{Engines: []config.Engine{
		{Kind: "redis", Extras: map[string]string{"backend": "docker"}},
	}}
	got := computeEngineBackends(cfg, map[string]bool{"redis": true})
	if got["redis"] != "docker" {
		t.Errorf("Backend[redis] = %q, want %q (extras.backend override was ignored)", got["redis"], "docker")
	}
}

// TestComputeEngineBackends_SkipsUnregisteredKinds keeps status from
// reporting a backend for an engine kind the registry has no row for
// (e.g. declared in the YAML but no worktree created yet) — that entry
// would describe something that does not exist.
func TestComputeEngineBackends_SkipsUnregisteredKinds(t *testing.T) {
	cfg := &config.Config{Engines: []config.Engine{
		{Kind: "mysql"}, // no backend declared, and NOT in registeredKinds below
	}}
	got := computeEngineBackends(cfg, map[string]bool{})
	if _, ok := got["mysql"]; ok {
		t.Errorf("Backend map contains %q for an engine kind absent from the registry: %v", "mysql", got)
	}
}

// TestComputeEngineBackends_OmittedBackendReportsDefault pins what an
// operator sees for a .bough.yaml that names no backend: the default
// the create path would stamp in, marked as such rather than presented
// as the operator's own choice.
func TestComputeEngineBackends_OmittedBackendReportsDefault(t *testing.T) {
	cfg := &config.Config{Engines: []config.Engine{{Kind: "mysql"}}}
	got := computeEngineBackends(cfg, map[string]bool{"mysql": true})
	if want := engineapi.DefaultBackend + " (default)"; got["mysql"] != want {
		t.Errorf("Backend for an omitted field = %q, want %q", got["mysql"], want)
	}
}

func TestEngineKindFromRegistryKey(t *testing.T) {
	cases := map[string]string{
		"mysql.main": "mysql",
		"mysql":      "mysql",
		"api":        "api",
	}
	for key, want := range cases {
		if got := engineKindFromRegistryKey(key); got != want {
			t.Errorf("engineKindFromRegistryKey(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestBuildStatus_NonEngineKindHasEmptyBackend(t *testing.T) {
	cfg := &config.Config{Engines: []config.Engine{{Kind: "mysql", Backend: "docker"}}}
	reg := map[string]map[string]int{
		"F-Test": {"mysql.main": 42000, "api": 45000},
	}
	out := buildStatus(reg, cfg)
	byKind := make(map[string]statusEntry, len(out))
	for _, e := range out {
		byKind[e.Kind] = e
	}
	if got := byKind["mysql.main"].Backend; got != "docker" {
		t.Errorf("mysql.main Backend = %q, want %q", got, "docker")
	}
	if got := byKind["api"].Backend; got != "" {
		t.Errorf("api (non-engine kind) Backend = %q, want empty", got)
	}
}
