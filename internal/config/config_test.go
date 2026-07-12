package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriveRepoName(t *testing.T) {
	cases := map[string]string{
		"git@github.com:eiicon-company/auba-proto":           "auba-proto",
		"git@github.com:eiicon-company/auba-proto.git":       "auba-proto",
		"https://github.com/eiicon-company/auba-dbmigration": "auba-dbmigration",
		"ssh://git@host/org/repo.git":                        "repo",
		"~/src/eiicon-company/claude/auba-proto":             "auba-proto",
		"../auba-proto/":                                     "auba-proto",
		"/abs/path/to/repo.git":                              "repo",
	}
	for in, want := range cases {
		if got := deriveRepoName(in); got != want {
			t.Errorf("deriveRepoName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLoad_SourceAndOptionalBranchStrategy: a source-only repo (no name,
// no branch_strategy) must load — name is derived from the source
// basename and branch_strategy is now optional (v0.9.16).
func TestLoad_SourceAndOptionalBranchStrategy(t *testing.T) {
	y := `schema_version: 2
monorepo_root: "."
repositories:
  - source: git@github.com:eiicon-company/auba-proto
registry:
  path: ".bough-ports.json"
`
	cfg, err := LoadFromBytes([]byte(y), "t.yaml")
	if err != nil {
		t.Fatalf("load source-only: %v", err)
	}
	if len(cfg.Repositories) != 1 || cfg.Repositories[0].Name != "auba-proto" {
		t.Fatalf("name not derived from source: %+v", cfg.Repositories)
	}
	if cfg.Repositories[0].BranchStrategy != "" {
		t.Errorf("branch_strategy should be empty (optional), got %q", cfg.Repositories[0].BranchStrategy)
	}
}

// TestLoad_RepoNeedsNameOrSource: a repo with neither name nor source is
// rejected with a clear semantic error.
func TestLoad_RepoNeedsNameOrSource(t *testing.T) {
	y := `schema_version: 2
monorepo_root: "."
repositories:
  - direnv: true
registry:
  path: ".bough-ports.json"
`
	_, err := LoadFromBytes([]byte(y), "t.yaml")
	if err == nil {
		t.Fatalf("expected error for repo with neither name nor source")
	}
	if !strings.Contains(err.Error(), "name") || !strings.Contains(err.Error(), "source") {
		t.Errorf("error should mention name/source: %v", err)
	}
}

// TestLoad_RejectsUnusableSource: a source whose basename derives to an
// empty or traversing name must be rejected, not silently leave Name=""
// (→ filepath.Join(root,"")=root) or Name=".."/"." (→ parent/root).
func TestLoad_RejectsUnusableSource(t *testing.T) {
	for _, src := range []string{".git", "/", "   ", ".", ".."} {
		y := "schema_version: 2\nmonorepo_root: \".\"\nrepositories:\n  - source: \"" + src + "\"\nregistry:\n  path: \".bough-ports.json\"\n"
		if _, err := LoadFromBytes([]byte(y), "t.yaml"); err == nil {
			t.Errorf("source %q should be rejected (derives empty/traversing name)", src)
		}
	}
}

// TestLoad_RejectsTraversalName: a name (typed or derived) that is not a
// single path segment must be rejected so worktree / `bough remove` git
// ops can never escape to the monorepo root, its parent, or elsewhere.
func TestLoad_RejectsTraversalName(t *testing.T) {
	// single-quoted YAML so a literal backslash survives (double-quoted
	// "a\b" would be parsed as a backspace escape, not the separator).
	for _, name := range []string{".", "..", "a/b", `a\b`} {
		y := "schema_version: 2\nmonorepo_root: \".\"\nrepositories:\n  - name: '" + name + "'\n    branch_strategy: develop\nregistry:\n  path: \".bough-ports.json\"\n"
		if _, err := LoadFromBytes([]byte(y), "t.yaml"); err == nil {
			t.Errorf("name %q should be rejected (not a single segment)", name)
		}
	}
}

// TestLoad_RejectsReservedName: a repo whose name collides with bough's
// own root-level layout dirs (worktrees / .worktrees / .bough) must be
// rejected, else `bough create`/`remove` would write into (or RemoveAll
// out of) that repo's checkout at <root>/<name>.
func TestLoad_RejectsReservedName(t *testing.T) {
	for _, name := range []string{"worktrees", ".worktrees", ".bough"} {
		y := "schema_version: 2\nmonorepo_root: \".\"\nrepositories:\n  - name: '" + name + "'\n    branch_strategy: develop\nregistry:\n  path: \".bough/ports.json\"\n"
		if _, err := LoadFromBytes([]byte(y), "t.yaml"); err == nil {
			t.Errorf("name %q should be rejected (reserved layout dir)", name)
		}
	}
	// a normal name that merely CONTAINS a reserved word is fine
	y := "schema_version: 2\nmonorepo_root: \".\"\nrepositories:\n  - name: 'my-worktrees-tool'\n    branch_strategy: develop\nregistry:\n  path: \".bough/ports.json\"\n"
	if _, err := LoadFromBytes([]byte(y), "t.yaml"); err != nil {
		t.Errorf("name 'my-worktrees-tool' must be accepted: %v", err)
	}
}

// TestLoad_EvolveClaudeMDOnSessionEnd covers the v0.9.14 opt-in flag:
// it must default to false when absent (= bough's no-repo-contamination
// default) and parse true when set under instinct:.
func TestLoad_EvolveClaudeMDOnSessionEnd(t *testing.T) {
	base, err := os.ReadFile(filepath.Join("testdata", "example.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	// default: field absent → false
	cfg, err := LoadFromBytes(base, "example.yaml")
	if err != nil {
		t.Fatalf("load base: %v", err)
	}
	if cfg.Instinct.EvolveClaudeMDOnSessionEnd {
		t.Errorf("default should be false when the field is absent")
	}
	// opt-in: field true → true
	withFlag := string(base) + "\ninstinct:\n  enabled: true\n  evolve_claudemd_on_session_end: true\n"
	cfg2, err := LoadFromBytes([]byte(withFlag), "example+flag.yaml")
	if err != nil {
		t.Fatalf("load with flag: %v", err)
	}
	if !cfg2.Instinct.EvolveClaudeMDOnSessionEnd {
		t.Errorf("evolve_claudemd_on_session_end: true did not parse into the struct")
	}
}

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

// TestLoad_ComposeEngine_roundTrip is the positive-path companion to
// the "compose kind" cases in TestLoad_rejectsInvalid: a well-formed
// compose: block must load and populate Engine.Compose verbatim.
func TestLoad_ComposeEngine_roundTrip(t *testing.T) {
	tmpdir := t.TempDir()
	path := filepath.Join(tmpdir, "config.yaml")
	yaml := `schema_version: 2
monorepo_root: "."
repositories:
  - {name: a, branch_strategy: develop, role: engine-provider}
engines:
  - kind: compose
    version: "7-alpine"
    port_ranges: {main: [56000, 56999]}
    compose:
      file: "auba-api/compose.yml"
      service: redis
      target_port: 6379
registry: {path: .bough-ports.json}
`
	if err := writeFile(t, path, yaml); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if got, want := len(c.Engines), 1; got != want {
		t.Fatalf("Engines: got %d want %d", got, want)
	}
	eng := c.Engines[0]
	if eng.Compose == nil {
		t.Fatalf("Engine.Compose is nil, want a populated *ComposeSpec")
	}
	if got, want := eng.Compose.File, "auba-api/compose.yml"; got != want {
		t.Errorf("Compose.File: got %q want %q", got, want)
	}
	if got, want := eng.Compose.Service, "redis"; got != want {
		t.Errorf("Compose.Service: got %q want %q", got, want)
	}
	if got, want := eng.Compose.TargetPort, 6379; got != want {
		t.Errorf("Compose.TargetPort: got %d want %d", got, want)
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
			// Regression guard: the plugin RPC wire narrows this value to
			// a proto int32, which would silently wrap negative for a
			// value at or above 2^31 with no validation error — the
			// operator's explicit timeout gets replaced by whatever the
			// plugin's own <=0 fallback default is, with no warning
			// anywhere in the chain.
			name: "ready_timeout_sec above the int32-safe cap",
			yaml: `schema_version: 2
monorepo_root: "."
repositories:
  - {name: a, branch_strategy: develop, role: engine-provider}
engines:
  - {kind: mysql, version: "8.4", port_ranges: {main: [42000, 44999]}, ready_timeout_sec: 999999999}
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
		{
			// kind: compose delegates to an existing docker-compose.yml
			// instead of provisioning its own engine — the plugin has
			// nothing to do without a compose: block naming the file/
			// service/target_port.
			name: "compose kind without compose block",
			yaml: `schema_version: 2
monorepo_root: "."
repositories:
  - {name: a, branch_strategy: develop, role: engine-provider}
engines:
  - {kind: compose, version: "7-alpine", port_ranges: {main: [56000, 56999]}}
registry: {path: .bough-ports.json}
`,
			wantInErr: "compose",
		},
		{
			// Compose is non-nil here, so this exercises ComposeSpec's
			// own struct-tag validation (v.Struct(c) in Validate()),
			// not the validateSemantic "Compose == nil" rule above.
			name: "compose kind with compose block missing file",
			yaml: `schema_version: 2
monorepo_root: "."
repositories:
  - {name: a, branch_strategy: develop, role: engine-provider}
engines:
  - {kind: compose, version: "7-alpine", port_ranges: {main: [56000, 56999]}, compose: {service: redis, target_port: 6379}}
registry: {path: .bough-ports.json}
`,
			wantInErr: "File",
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

// TestLoad_migrateLegacy_MergesEnginesAndDatabases is the regression
// guard for the #17-review finding: migrateLegacy used to overwrite
// c.Engines (already populated from the YAML's engines: section) with
// the databases: conversion whenever databases: was non-empty,
// silently discarding any engines: entries declared in the same file
// — breaking the exact incremental-migration story ("existing
// deployments do not have to migrate in lockstep") schema_version: 1
// is meant to support: keep an existing engine under the legacy
// databases: section while adding a new one under engines:.
func TestLoad_migrateLegacy_MergesEnginesAndDatabases(t *testing.T) {
	yaml := `schema_version: 1
monorepo_root: "."
repositories:
  - {name: dbrepo, branch_strategy: develop, role: db-provider}
databases:
  - kind: mysql
    version: "8.4"
    port_range: [42000, 44999]
engines:
  - kind: rabbitmq
    version: "3.13"
    port_ranges: {main: [60000, 60999]}
registry: {path: .worktree-ports.json}
`
	c, err := LoadFromBytes([]byte(yaml), "test-legacy-mixed")
	if err != nil {
		t.Fatalf("LoadFromBytes(mixed legacy): %v", err)
	}
	if got, want := len(c.Engines), 2; got != want {
		t.Fatalf("Engines: got %d want %d (engines: entry must survive alongside the databases: conversion): %+v", got, want, c.Engines)
	}
	var sawMysql, sawRabbitmq bool
	for _, eng := range c.Engines {
		switch eng.Kind {
		case "mysql":
			sawMysql = true
		case "rabbitmq":
			sawRabbitmq = true
		}
	}
	if !sawMysql {
		t.Error("mysql (from databases:) missing after migration")
	}
	if !sawRabbitmq {
		t.Error("rabbitmq (from engines:) missing after migration — engines: entries were dropped")
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

// TestLoad_acceptsQualityGates pins the v0.7.1 quality_gates root
// section so the LegacyConfig superset migration carries each gate
// declaration through to the canonical Config. The Gate runner
// (internal/qualitygate) reads from c.QualityGates, so this guard
// is what makes `bough config validate` accept the new section.
func TestLoad_acceptsQualityGates(t *testing.T) {
	yaml := `schema_version: 2
monorepo_root: "."
repositories:
  - name: demo
    branch_strategy: develop
registry:
  path: .bough-ports.json
quality_gates:
  - name: typecheck
    command: "nix develop -c make test-short"
    on_event: PostToolUse
    on_tool: Edit
    on_match: ".*\\.go$"
    timeout_seconds: 120
  - name: lint
    command: "golangci-lint run --new-from-rev=HEAD~1"
    on_event: PostToolUse
`
	c, err := LoadFromBytes([]byte(yaml), "test-v071-quality-gates")
	if err != nil {
		t.Fatalf("LoadFromBytes(quality_gates): %v", err)
	}
	if got, want := len(c.QualityGates), 2; got != want {
		t.Fatalf("QualityGates length: got %d want %d", got, want)
	}
	if got, want := c.QualityGates[0].Name, "typecheck"; got != want {
		t.Errorf("QualityGates[0].Name: got %q want %q", got, want)
	}
	if got, want := c.QualityGates[0].TimeoutSeconds, 120; got != want {
		t.Errorf("QualityGates[0].TimeoutSeconds: got %d want %d", got, want)
	}
}
