package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLoad_validExample feeds the legacy v0.3 testdata fixture into
// the v0.4 loader, exercising migrateLegacy()'s `databases:` →
// `engines:`, `port_range:` → `port_ranges:{main:...}`,
// `initial_databases:` → `initial_resources:[{type:database,...}]`
// auto-conversion path. Once auba ships a v0.4-canonical fixture the
// expected SchemaVersion bumps to 2 and the assertions stay otherwise
// identical.
func TestLoad_validExample(t *testing.T) {
	c, err := Load(filepath.Join("testdata", "example.yaml"))
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if got, want := c.SchemaVersion, 1; got != want {
		t.Errorf("SchemaVersion: got %d want %d", got, want)
	}
	if got, want := len(c.Repositories), 3; got != want {
		t.Fatalf("Repositories: got %d want %d", got, want)
	}
	var sawProvider bool
	for _, r := range c.Repositories {
		if r.Role == "engine-provider" || r.Role == "db-provider" {
			sawProvider = true
		}
	}
	if !sawProvider {
		t.Errorf("expected exactly one repository with role: engine-provider (or legacy db-provider)")
	}
	if got, want := len(c.Engines), 1; got != want {
		t.Fatalf("Engines: got %d want %d", got, want)
	}
	if got, want := c.Engines[0].Kind, "mysql"; got != want {
		t.Errorf("Engine.Kind: got %q want %q", got, want)
	}
	if got, want := c.Engines[0].PortRanges["main"], [2]int{42000, 44999}; got != want {
		t.Errorf("Engine.PortRanges[main]: got %v want %v", got, want)
	}
	if got, want := c.Engines[0].ReadyTimeoutSec, 900; got != want {
		t.Errorf("Engine.ReadyTimeoutSec: got %d want %d", got, want)
	}
}

// Each entry exercises one of the validateSemantic / struct-tag
// failure modes. The test asserts both that Load returns an error and
// that the error message contains an identifying substring, so a
// future drift in validator output is caught without forcing exact-
// string matches.
//
// v0.4 note: cases using `databases:` / `port_range:` / `db-provider`
// keep the legacy keys because migrateLegacy() preserves the
// validation surface — an invalid legacy YAML must still fail after
// auto-conversion, otherwise we have silently widened the contract.
func TestLoad_rejectsInvalid(t *testing.T) {
	cases := []struct {
		name      string
		yaml      string
		wantInErr string
	}{
		{
			name: "missing schema_version",
			yaml: `monorepo_root: "."
repositories:
  - {name: a, branch_strategy: develop}
registry: {path: .bough-ports.json}
`,
			wantInErr: "SchemaVersion",
		},
		{
			name: "zero repositories",
			yaml: `schema_version: 1
monorepo_root: "."
repositories: []
registry: {path: .bough-ports.json}
`,
			wantInErr: "Repositories",
		},
		{
			name: "engine without engine-provider repo",
			yaml: `schema_version: 2
monorepo_root: "."
repositories:
  - {name: a, branch_strategy: develop}
engines:
  - {kind: mysql, version: "8.4", port_ranges: {main: [42000, 44999]}}
registry: {path: .bough-ports.json}
`,
			wantInErr: "engine-provider",
		},
		{
			name: "duplicate repository name",
			yaml: `schema_version: 1
monorepo_root: "."
repositories:
  - {name: a, branch_strategy: develop}
  - {name: a, branch_strategy: develop}
registry: {path: .bough-ports.json}
`,
			wantInErr: "duplicated",
		},
		{
			name: "invalid port range",
			yaml: `schema_version: 2
monorepo_root: "."
repositories:
  - {name: a, branch_strategy: develop, role: engine-provider}
engines:
  - {kind: mysql, version: "8.4", port_ranges: {main: [42000, 42000]}}
registry: {path: .bough-ports.json}
`,
			wantInErr: "port_ranges",
		},
		{
			name: "invalid role value",
			yaml: `schema_version: 1
monorepo_root: "."
repositories:
  - {name: a, branch_strategy: develop, role: invalid-role}
registry: {path: .bough-ports.json}
`,
			wantInErr: "Role",
		},
		{
			name: "negative ready_timeout_sec",
			yaml: `schema_version: 2
monorepo_root: "."
repositories:
  - {name: a, branch_strategy: develop, role: engine-provider}
engines:
  - {kind: mysql, version: "8.4", port_ranges: {main: [42000, 44999]}, ready_timeout_sec: -1}
registry: {path: .bough-ports.json}
`,
			wantInErr: "ReadyTimeoutSec",
		},
		{
			name: "unknown top-level field (strict mode)",
			yaml: `schema_version: 1
monorepo_root: "."
typo_field: 1
repositories:
  - {name: a, branch_strategy: develop}
registry: {path: .bough-ports.json}
`,
			wantInErr: "typo_field",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpdir := t.TempDir()
			path := filepath.Join(tmpdir, "config.yaml")
			if err := writeFile(t, path, tc.yaml); err != nil {
				t.Fatalf("writeFile: %v", err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantInErr)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantInErr)
			}
		})
	}
}

// TestLoad_migrateLegacy_v03 asserts the auto-conversion path:
// a schema_version: 1 YAML with databases: / port_range: /
// initial_databases: must materialise as engines: / port_ranges: {
// main: [...] } / initial_resources: [ {type: database, name: <s>} ]
// in memory. Removed in v0.5.0 alongside the legacy fallback itself.
func TestLoad_migrateLegacy_v03(t *testing.T) {
	yaml := `schema_version: 1
monorepo_root: "."
repositories:
  - {name: dbrepo, branch_strategy: develop, role: db-provider}
databases:
  - kind: mysql
    version: "8.4"
    port_range: [42000, 44999]
    initial_databases: [auba, replica]
registry: {path: .worktree-ports.json}
`
	c, err := LoadFromBytes([]byte(yaml), "test-legacy")
	if err != nil {
		t.Fatalf("LoadFromBytes(legacy): %v", err)
	}
	if got, want := len(c.Engines), 1; got != want {
		t.Fatalf("Engines: got %d want %d", got, want)
	}
	eng := c.Engines[0]
	if got, want := eng.Kind, "mysql"; got != want {
		t.Errorf("Kind: got %q want %q", got, want)
	}
	if got, want := eng.PortRanges["main"], [2]int{42000, 44999}; got != want {
		t.Errorf("PortRanges[main]: got %v want %v", got, want)
	}
	if got, want := len(eng.InitialResources), 2; got != want {
		t.Fatalf("InitialResources: got %d want %d", got, want)
	}
	if got, want := eng.InitialResources[0].Type, "database"; got != want {
		t.Errorf("InitialResources[0].Type: got %q want %q", got, want)
	}
	if got, want := eng.InitialResources[0].Name, "auba"; got != want {
		t.Errorf("InitialResources[0].Name: got %q want %q", got, want)
	}
	if got, want := eng.InitialResources[1].Name, "replica"; got != want {
		t.Errorf("InitialResources[1].Name: got %q want %q", got, want)
	}
}

// TestLoad_acceptsV05Sections pins the LegacyConfig superset against
// the v0.5+ root sections (`instinct`, `engines`, `memory_backends`,
// `export`). Post-ship dogfooding on 2026-06-22 surfaced that the
// strict first-pass decode of `bough config validate` was rejecting
// v0.5+ YAML with `unknown field` while every other subcommand loaded
// the file cleanly through a separate entry point — LegacyConfig had
// been frozen at the v0.3+v0.4 superset and the v0.5 schema bump did
// not mirror the new sections in. Regression backstop: a YAML that
// uses all four v0.5+ sections must parse, migrate, and validate
// without complaint.
func TestLoad_acceptsV05Sections(t *testing.T) {
	yaml := `schema_version: 2
monorepo_root: "."
repositories:
  - name: demo
    branch_strategy: develop
engines: []
registry:
  path: .bough-ports.json
instinct:
  enabled: true
  default_memory_backend: sqlite
  fallback_on_error: false
  retrieve:
    max_results: 12
    max_tokens: 4000
    min_confidence: 0.4
  mint:
    mode: hybrid
    require_approval: true
    redaction:
      enabled: true
  plugin_security:
    require_signed: false
    accepted_signature_schemes:
      - cosign
      - minisign
memory_backends:
  - kind: sqlite
    role: reference-fallback
    path: .bough/memory/instincts.db
    fts: true
    wal: true
    busy_timeout_ms: 5000
export:
  formats: [agent-skill]
  output_dir: ./skills
`
	c, err := LoadFromBytes([]byte(yaml), "test-v05-sections")
	if err != nil {
		t.Fatalf("LoadFromBytes(v0.5+ sections): %v", err)
	}
	if !c.Instinct.Enabled {
		t.Errorf("Instinct.Enabled: want true")
	}
	if got, want := c.Instinct.DefaultMemoryBackend, "sqlite"; got != want {
		t.Errorf("Instinct.DefaultMemoryBackend: got %q want %q", got, want)
	}
	if got, want := c.Instinct.Retrieve.MaxResults, 12; got != want {
		t.Errorf("Instinct.Retrieve.MaxResults: got %d want %d", got, want)
	}
	if got, want := c.Instinct.Mint.Mode, "hybrid"; got != want {
		t.Errorf("Instinct.Mint.Mode: got %q want %q", got, want)
	}
	if got, want := len(c.Instinct.PluginSecurity.AcceptedSignatureSchemes), 2; got != want {
		t.Errorf("Instinct.PluginSecurity.AcceptedSignatureSchemes: got %d want %d", got, want)
	}
	if got, want := len(c.MemoryBackends), 1; got != want {
		t.Fatalf("MemoryBackends: got %d want %d", got, want)
	}
	if got, want := c.MemoryBackends[0].Kind, "sqlite"; got != want {
		t.Errorf("MemoryBackends[0].Kind: got %q want %q", got, want)
	}
	if got, want := c.MemoryBackends[0].Role, "reference-fallback"; got != want {
		t.Errorf("MemoryBackends[0].Role: got %q want %q", got, want)
	}
	if got, want := len(c.Export.Formats), 1; got != want {
		t.Errorf("Export.Formats: got %d want %d", got, want)
	}
	if got, want := c.Export.OutputDir, "./skills"; got != want {
		t.Errorf("Export.OutputDir: got %q want %q", got, want)
	}
}
