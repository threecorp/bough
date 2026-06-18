// Package config defines the on-disk YAML schema (`.bough.yaml`) that
// drives the bough orchestrator and the loader / validator that the
// host CLI consumes.
//
// The schema is fully declarative — a monorepo declares its sub-repos,
// the engines it wants per-worktree, and the per-kind port ranges.
// The host iterates over Config.Repositories to drive `git worktree
// add`, `direnv allow`, `.env.local` rendering, and post-create hooks.
// It iterates over Config.Engines to spawn the matching
// `bough-plugin-<kind>` gRPC plugin and call its lifecycle methods.
// Neither host nor plugin owns a copy of the per-monorepo policy — it
// lives entirely in this YAML.
//
// v0.4.0 schema change: section `databases:` → `engines:`, fields
// `port_range: [a,b]` → `port_ranges: {main: [a,b]}` and
// `initial_databases: [...]` → `initial_resources: [...]`. The legacy
// v0.3 keys are accepted as aliases during the v0.4.x transition with
// a deprecated warning; removed in v0.5.0. See
// docs/MIGRATION-v0.3-to-v0.4.md.
//
// Validation is performed via github.com/go-playground/validator/v10
// struct tags. Custom semantic rules (e.g. "exactly one
// engine-provider repo when at least one Engines entry is set") live
// in validate.go.
package config

import (
	"fmt"
	"os"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

// Config is the root of `.bough.yaml`.
//
// `schema_version` is required so the loader can refuse forward-
// incompatible files instead of silently mapping unknown fields to
// zero values. v0.4.0 accepts schema_version 1 (= v0.3-era, auto-
// migrated to v2 in memory) or 2 (= v0.4 canonical). Schema 1 reads
// `databases:` etc. with a deprecated warning.
type Config struct {
	SchemaVersion int                  `yaml:"schema_version" validate:"required,oneof=1 2"`
	MonorepoRoot  string               `yaml:"monorepo_root" validate:"required"`
	Repositories  []Repository         `yaml:"repositories" validate:"required,min=1,dive"`
	Engines       []Engine             `yaml:"engines" validate:"dive"`
	Ports         map[string]PortRange `yaml:"ports" validate:"dive"`
	Registry      RegistryConfig       `yaml:"registry" validate:"required"`
	Teardown      TeardownConfig       `yaml:"teardown"`
	MCP           MCPConfig            `yaml:"mcp"`
}

// Repository declares one git sub-repo that hangs off
// `.worktrees/<name>/`.
//
// Role values:
//   - "" (empty): the worktree is created and direnv-allowed but
//     receives no `.env.local` injection beyond `EnvLocal` (used for
//     proto / build-tool repos that have no port dependency).
//   - "engine-provider" (v0.4) / "db-provider" (v0.3 alias): the
//     worktree owns the per-worktree engine datadir and is the cwd
//     from which the bough host issues `nix run --impure '.#mysql'
//     -- up`. Exactly one repository per Config carries this role
//     when at least one `engines:` entry is present.
type Repository struct {
	Name           string            `yaml:"name" validate:"required"`
	BranchStrategy string            `yaml:"branch_strategy" validate:"required"`
	Direnv         bool              `yaml:"direnv"`
	Role           string            `yaml:"role" validate:"omitempty,oneof=engine-provider db-provider"`
	Symlinks       []SymlinkSpec     `yaml:"symlinks" validate:"dive"`
	EnvLocal       map[string]string `yaml:"env_local"`
	PostCreate     []string          `yaml:"post_create"`
	PreRemove      []string          `yaml:"pre_remove"`
}

// Engine picks a `bough-plugin-<Kind>` binary (Hashicorp go-plugin
// gRPC) and supplies its per-instance parameters.
//
// `port_ranges` overrides the plugin's per-role defaults. Single-port
// engines (mysql / postgres / redis / elasticsearch) declare
// `port_ranges: { main: [low, high] }`. Multi-port engines (rabbitmq,
// kafka, nats) declare one entry per role.
type Engine struct {
	Kind             string             `yaml:"kind" validate:"required"`
	Version          string             `yaml:"version" validate:"required"`
	PortRanges       map[string][2]int  `yaml:"port_ranges" validate:"required,min=1"`
	SocketDir        string             `yaml:"socket_dir"`
	InitialResources []InitialResource  `yaml:"initial_resources" validate:"dive"`
	// Backend selects the lifecycle implementation inside the plugin.
	// Allowed values: "nix" (default for v0.1.x), "docker" (v0.2+,
	// bind-mounts datadir into the engine's official Docker image),
	// or empty for "auto-detect" (= the host's hybrid selector picks
	// based on runtime detection: nix-with-flakes on PATH → nix,
	// else docker daemon → docker).
	Backend string `yaml:"backend" validate:"omitempty,oneof=nix docker"`
	// ReadyTimeoutSec caps how long the host waits for the plugin's
	// ReadyCheck loop to report ready. Zero means use the plugin's
	// own default (typically 300-600 s).
	ReadyTimeoutSec int               `yaml:"ready_timeout_sec" validate:"omitempty,min=1"`
	Extras          map[string]string `yaml:"extras"`
}

// InitialResource describes one resource (DB schema, kafka topic,
// minio bucket, rabbitmq vhost, consul KV, ...) the plugin should
// provision at Up time. Type discriminates the kind; Params carries
// engine-specific tuning (kafka topic partitions, postgres encoding,
// etc.).
type InitialResource struct {
	Type   string            `yaml:"type" validate:"required"`
	Name   string            `yaml:"name" validate:"required"`
	Params map[string]string `yaml:"params"`
}

// PortRange covers all non-engine port kinds (api / gateway / view /
// ...). Engine port ranges live inside `Engine.PortRanges` because
// they are owned by the plugin, not the host.
type PortRange struct {
	Range [2]int `yaml:"range" validate:"required"`
}

// RegistryConfig points at the `.bough-ports.json` atomic registry
// that holds the deterministic port allocation per branch.
type RegistryConfig struct {
	Path      string `yaml:"path" validate:"required"`
	BackupDir string `yaml:"backup_dir"`
}

// TeardownConfig governs `bough remove` behaviour.
type TeardownConfig struct {
	RemoveBranch       bool `yaml:"remove_branch"`
	RemoveDatadir      bool `yaml:"remove_datadir"`
	GracefulTimeoutSec int  `yaml:"graceful_timeout_sec" validate:"omitempty,min=1"`
}

// MCPConfig wires `~/.claude.json` projects-entry bootstrap so a
// Claude Code session opened inside a new worktree sees the same MCP
// servers as the parent monorepo. Disabled by default — opt in by
// setting `enabled: true`.
type MCPConfig struct {
	Enabled       bool   `yaml:"enabled"`
	SourceOfTruth string `yaml:"source_of_truth"`
}

// SymlinkSpec declares one symlink to drop into the worktree root
// after `git worktree add` (typically used to re-expose CLAUDE.md so
// edits in the worktree reflect back to the monorepo root copy).
type SymlinkSpec struct {
	Target string `yaml:"target" validate:"required"`
	Link   string `yaml:"link" validate:"required"`
}

// LegacyConfig mirrors Config's shape with the v0.3 field names so
// `databases:` / `initial_databases:` / `port_range:` deserialise
// without error. After deserialisation the LoadFromBytes path calls
// migrateLegacy() to copy values into the canonical Config fields.
type LegacyConfig struct {
	SchemaVersion int                  `yaml:"schema_version"`
	MonorepoRoot  string               `yaml:"monorepo_root"`
	Repositories  []Repository         `yaml:"repositories"`
	Databases     []LegacyDatabase     `yaml:"databases"`
	Ports         map[string]PortRange `yaml:"ports"`
	Registry      RegistryConfig       `yaml:"registry"`
	Teardown      TeardownConfig       `yaml:"teardown"`
	MCP           MCPConfig            `yaml:"mcp"`
}

// LegacyDatabase is the v0.3 shape of one `databases:` entry. The
// migration step converts each entry into an Engine. Removed in
// v0.5.0.
type LegacyDatabase struct {
	Kind             string            `yaml:"kind"`
	Version          string            `yaml:"version"`
	PortRange        [2]int            `yaml:"port_range"`
	SocketDir        string            `yaml:"socket_dir"`
	InitialDatabases []string          `yaml:"initial_databases"`
	Backend          string            `yaml:"backend"`
	ReadyTimeoutSec  int               `yaml:"ready_timeout_sec"`
	Extras           map[string]string `yaml:"extras"`
}

// Load reads `.bough.yaml` (v0.4 canonical) when present, otherwise
// falls back to `.worktree-isolation.yaml` (v0.3 legacy) with a
// deprecated warning. The chosen path is passed to LoadFromPath so
// the host can rely on Load to do the discovery.
//
// `strict` decoding is enabled so a typo in a field name (e.g.
// `repositries:` instead of `repositories:`) raises a hard error
// instead of silently dropping the entry — config drift is otherwise
// extremely hard to debug once a worktree is spawned against a
// half-applied policy.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return LoadFromBytes(raw, path)
}

// LoadFromBytes parses + validates a YAML payload. The `pathHint` is
// included in error messages so the caller knows which file failed
// even when only bytes were passed in.
func LoadFromBytes(raw []byte, pathHint string) (*Config, error) {
	// Two-pass decode: first into LegacyConfig (which accepts both v0.3
	// and v0.4 field names because v0.4 fields are additive), so the
	// loader does not have to dispatch on schema_version before parsing.
	// Then we copy / migrate into the canonical Config.
	dec := yaml.NewDecoder(newByteReader(raw))
	dec.KnownFields(true) // strict
	var lc LegacyConfig
	if err := dec.Decode(&lc); err != nil {
		// Fall back to a v0.4-strict decode so the user sees the
		// canonical error message rather than a confusing complaint
		// about an unknown `databases:` field.
		var c Config
		dec2 := yaml.NewDecoder(newByteReader(raw))
		dec2.KnownFields(true)
		if err2 := dec2.Decode(&c); err2 != nil {
			return nil, fmt.Errorf("parse %s: %w", pathHint, err)
		}
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("validate %s: %w", pathHint, err)
		}
		return &c, nil
	}

	c, warnings := migrateLegacy(&lc)
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "bough: WARNING %s\n", w)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("validate %s: %w", pathHint, err)
	}
	return c, nil
}

// migrateLegacy converts a v0.3 LegacyConfig (which also happens to
// be a superset of v0.4) into a canonical Config, emitting a
// deprecated warning per old field actually used. Removed in v0.5.0.
//
// Conversion rules (see project_bough_v04_yaml_migration_rules memory):
//   - `databases:` → `engines:` (one-to-one)
//   - `port_range: [low, high]` → `port_ranges: {main: [low, high]}`
//   - `initial_databases: ["a"]` → `initial_resources: [{type:
//     database, name: a}]`
func migrateLegacy(lc *LegacyConfig) (*Config, []string) {
	var warnings []string
	c := &Config{
		SchemaVersion: lc.SchemaVersion,
		MonorepoRoot:  lc.MonorepoRoot,
		Repositories:  lc.Repositories,
		Ports:         lc.Ports,
		Registry:      lc.Registry,
		Teardown:      lc.Teardown,
		MCP:           lc.MCP,
	}
	if lc.SchemaVersion == 1 {
		warnings = append(warnings,
			"schema_version: 1 is deprecated, bump to 2 once you have renamed databases:→engines:, port_range:→port_ranges:, initial_databases:→initial_resources: (removed in v0.5.0)")
	}
	if len(lc.Databases) > 0 {
		warnings = append(warnings,
			"YAML section 'databases:' is deprecated, rename to 'engines:' (auto-converted for now; removed in v0.5.0)")
		c.Engines = make([]Engine, len(lc.Databases))
		for i, db := range lc.Databases {
			c.Engines[i] = Engine{
				Kind:            db.Kind,
				Version:         db.Version,
				PortRanges:      map[string][2]int{"main": db.PortRange},
				SocketDir:       db.SocketDir,
				Backend:         db.Backend,
				ReadyTimeoutSec: db.ReadyTimeoutSec,
				Extras:          db.Extras,
			}
			if len(db.InitialDatabases) > 0 {
				c.Engines[i].InitialResources = make([]InitialResource, len(db.InitialDatabases))
				for j, dbname := range db.InitialDatabases {
					c.Engines[i].InitialResources[j] = InitialResource{
						Type: "database",
						Name: dbname,
					}
				}
			}
		}
	}
	// `engine-provider` is the canonical role name as of v0.4; if the
	// YAML still says `db-provider` we accept it but warn once.
	for _, r := range c.Repositories {
		if r.Role == "db-provider" {
			warnings = append(warnings,
				"repositories[*].role: 'db-provider' is deprecated, rename to 'engine-provider' (auto-accepted for now; removed in v0.5.0)")
			break
		}
	}
	return c, warnings
}

// Validate runs go-playground/validator over the struct tags plus the
// semantic rules in validate.go. Exported so unit tests and the
// `bough config validate` CLI subcommand can reuse it.
func (c *Config) Validate() error {
	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(c); err != nil {
		return err
	}
	return c.validateSemantic()
}
