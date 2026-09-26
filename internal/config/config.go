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
	"strings"

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
// `worktrees/<name>/`.
//
// Role values:
//   - "" (empty): the worktree is created and direnv-allowed but
//     receives no `.env.local` injection beyond `EnvLocal` (used for
//     proto / build-tool repos that have no port dependency).
//   - "engine-provider" (v0.4) / "db-provider" (v0.3 alias): the
//     worktree owns the per-worktree engine datadir and is the
//     WorktreeRoot handed to each engine's Up. Exactly one repository
//     per Config carries this role when at least one `engines:` entry
//     is present.
type Repository struct {
	// Name is the sub-directory under the monorepo root (and under each
	// worktree) this repo lives in. Optional when Source is set — it is
	// then derived from the Source basename (e.g. source
	// git@github.com:org/auba-proto → name "auba-proto"). At least one of
	// Name / Source must be present.
	Name string `yaml:"name"`
	// Source, when set, is where bough acquires the repo from if
	// <monorepoRoot>/<name> does not already exist: a remote git URL
	// (git@host:org/repo, https://…, ssh://…) → `git clone`, or a local
	// path (~/…, ./…, ../…, /abs) → `git clone --local`. Empty = the repo
	// must already be present (the historical behaviour).
	Source string `yaml:"source"`
	// BranchStrategy is the branch new worktrees are cut from. Optional:
	// when empty, bough uses the repo's default branch (origin/HEAD). An
	// explicit value is authoritative over origin/HEAD (see chooseBase).
	BranchStrategy string            `yaml:"branch_strategy"`
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
	Kind             string            `yaml:"kind" validate:"required"`
	Version          string            `yaml:"version" validate:"required"`
	PortRanges       map[string][2]int `yaml:"port_ranges" validate:"required,min=1"`
	SocketDir        string            `yaml:"socket_dir"`
	InitialResources []InitialResource `yaml:"initial_resources" validate:"dive"`
	// Backend selects the lifecycle implementation inside the plugin.
	// "docker" is the only one the bundled plugins register, and the
	// one an omitted field resolves to. Validated here (the literal
	// mirrors engineapi.DefaultBackend, which a struct tag cannot
	// reference) so a stale value fails at load rather than at Up.
	Backend string `yaml:"backend" validate:"omitempty,oneof=docker"`
	// ReadyTimeoutSec caps how long the host waits for the plugin's
	// ReadyCheck loop to report ready. Zero means use the plugin's
	// own default (typically 300-600 s). Capped well under int32 max:
	// the value crosses the wire to the plugin as a proto int32, and
	// an unbounded value here could silently wrap negative there.
	ReadyTimeoutSec int               `yaml:"ready_timeout_sec" validate:"omitempty,min=1,max=86400"`
	Extras          map[string]string `yaml:"extras"`
	// Compose carries the parameters for kind: compose — a plugin that
	// wraps an existing docker-compose file/service instead of
	// provisioning its own engine. Nil for every other kind.
	// validateSemantic requires it (non-nil, File/Service set) exactly
	// when Kind == "compose".
	Compose *ComposeSpec `yaml:"compose"`
	// Plugins lists engine plugins (e.g. an Elasticsearch analyzer) to
	// make available before Up. Ignored by engines that do not
	// implement plugin management (mysql, redis, postgres, compose).
	Plugins []EnginePlugin `yaml:"plugins" validate:"dive"`
}

// ComposeSpec identifies the pre-existing docker-compose file/service
// a kind: compose Engine wraps. File is resolved by the plugin
// relative to the raw worktree root (the directory containing every
// declared repository as a sibling), not the engine-provider repo's
// own worktree path.
type ComposeSpec struct {
	File       string `yaml:"file" validate:"required"`
	Service    string `yaml:"service" validate:"required"`
	TargetPort int    `yaml:"target_port" validate:"required,min=1,max=65535"`
	// Project overrides the auto-derived, worktree-scoped compose
	// project name (default: "bough-<worktree>-<service>").
	Project string `yaml:"project"`
	// EnvPrefix overrides the env-var prefix EnvVars() emits
	// (BOUGH_<PREFIX>_HOST/_PORT/_URL). Defaults to
	// strings.ToUpper(Service).
	EnvPrefix string `yaml:"env_prefix"`
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

// EnginePlugin describes one plugin bough should make available to the
// engine before it starts (e.g. an Elasticsearch analyzer). ID and
// Location mirror elasticsearch-plugins.yml's own field names 1:1 so
// the elasticsearch plugin can re-marshal this value directly into
// that file. Location is required for unofficial/third-party plugins
// (a direct download URL) and left empty for official plugins the
// engine's own registry already knows by ID.
type EnginePlugin struct {
	ID       string `yaml:"id" validate:"required"`
	Location string `yaml:"location"`
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

// LegacyConfig mirrors Config's shape with both the v0.3 field names
// (so `databases:` / `initial_databases:` / `port_range:` deserialise
// without error) and the v0.4+ canonical field names. After
// deserialisation the LoadFromBytes path calls migrateLegacy() to copy
// values into the canonical Config struct.
//
// Post-v0.5 dogfooding finding (2026-06-22): the v0.5 schema bump added
// `instinct:` / `memory_backends:` / `engines:` / `export:` root
// sections but did not mirror them into this superset, so the strict
// first-pass decode rejected every v0.5+ `.bough.yaml`. Every other
// subcommand decoded the file fine through a separate entry point, but
// `bough config validate` reported a false-negative. Three of those four
// sections are retired as of v0.28.0 and are held below as opaque
// yaml.Node fields — still accepted, warned about once, and NOT copied
// into Config, which has no field for them.
type LegacyConfig struct {
	SchemaVersion int                  `yaml:"schema_version"`
	MonorepoRoot  string               `yaml:"monorepo_root"`
	Repositories  []Repository         `yaml:"repositories"`
	Databases     []LegacyDatabase     `yaml:"databases"`
	Engines       []Engine             `yaml:"engines"`
	Ports         map[string]PortRange `yaml:"ports"`
	Registry      RegistryConfig       `yaml:"registry"`
	Teardown      TeardownConfig       `yaml:"teardown"`
	MCP           MCPConfig            `yaml:"mcp"`

	// Sections that configured the continuous-learning loop bough
	// carried until v0.27.0. Decoded as opaque nodes and never read:
	// the decoder is strict, so without a field here a `.bough.yaml`
	// that merely still carries one of these lines would fail to parse
	// and take `claude --worktree` down with it. yaml.Node accepts both
	// shapes that occur (a mapping for three of them, a sequence for
	// quality_gates), and !IsZero() is what migrateLegacy warns on.
	// Removed in v0.29.0.
	RetiredInstinct       yaml.Node `yaml:"instinct"`
	RetiredMemoryBackends yaml.Node `yaml:"memory_backends"`
	RetiredExport         yaml.Node `yaml:"export"`
	RetiredQualityGates   yaml.Node `yaml:"quality_gates"`
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

// Load reads and parses the config file at the given path (already
// resolved by the caller — the `.bough.yaml` vs `.worktree-isolation.yaml`
// v0.4/v0.3 discovery-with-deprecation-warning logic lives one layer up,
// in internal/cli/helpers.go's resolveConfigPath, not here).
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
	warnings = append(warnings, c.deprecationWarnings()...)
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
		Engines:       lc.Engines,
		Ports:         lc.Ports,
		Registry:      lc.Registry,
		Teardown:      lc.Teardown,
		MCP:           lc.MCP,
	}
	// One line per retired section the file still carries. Written here
	// rather than in deprecationWarnings() because only the legacy decode
	// sees these nodes — Config has no field for them by design.
	for _, r := range []struct {
		node yaml.Node
		key  string
	}{
		{lc.RetiredInstinct, "instinct"},
		{lc.RetiredMemoryBackends, "memory_backends"},
		{lc.RetiredExport, "export"},
		{lc.RetiredQualityGates, "quality_gates"},
	} {
		if !r.node.IsZero() {
			warnings = append(warnings, fmt.Sprintf(
				"YAML section '%s:' is retired and does nothing: the continuous-learning loop it configured was removed in v0.28.0; delete the section (the key stops parsing in v0.29.0)", r.key))
		}
	}
	if lc.SchemaVersion == 1 {
		warnings = append(warnings,
			"schema_version: 1 is deprecated, bump to 2 once you have renamed databases:→engines:, port_range:→port_ranges:, initial_databases:→initial_resources: (removed in v0.5.0)")
	}
	if len(lc.Databases) > 0 {
		warnings = append(warnings,
			"YAML section 'databases:' is deprecated, rename to 'engines:' (auto-converted for now; removed in v0.5.0)")
		// Append the converted databases: entries to whatever engines:
		// already held (set from lc.Engines above) rather than
		// replacing it — an incremental v0.3→v0.4 migration can declare
		// both sections in the same file (existing engines still under
		// databases:, a new one added under engines:), and overwriting
		// here silently dropped the engines: entries.
		converted := make([]Engine, len(lc.Databases))
		for i, db := range lc.Databases {
			converted[i] = Engine{
				Kind:            db.Kind,
				Version:         db.Version,
				PortRanges:      map[string][2]int{"main": db.PortRange},
				SocketDir:       db.SocketDir,
				Backend:         db.Backend,
				ReadyTimeoutSec: db.ReadyTimeoutSec,
				Extras:          db.Extras,
			}
			if len(db.InitialDatabases) > 0 {
				converted[i].InitialResources = make([]InitialResource, len(db.InitialDatabases))
				for j, dbname := range db.InitialDatabases {
					converted[i].InitialResources[j] = InitialResource{
						Type: "database",
						Name: dbname,
					}
				}
			}
		}
		c.Engines = append(c.Engines, converted...)
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
	c.normalizeRepositories()
	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(c); err != nil {
		return err
	}
	return c.validateSemantic()
}

// normalizeRepositories fills each repository's Name from its Source
// basename when Name is empty, so a `- source: …` entry with no explicit
// name still resolves to a sub-directory. Idempotent; runs before every
// Validate so all downstream code (create / remove / envwriter) can rely
// on Name being populated.
func (c *Config) normalizeRepositories() {
	for i := range c.Repositories {
		r := &c.Repositories[i]
		if r.Name == "" && r.Source != "" {
			r.Name = deriveRepoName(r.Source)
		}
	}
}

// deriveRepoName extracts a repo directory name from a Source URL/path:
// the last path segment, with any trailing slash and `.git` suffix
// stripped. Handles git@host:org/repo, https://host/org/repo, and local
// paths (~/…, ./…, /abs) alike.
func deriveRepoName(source string) string {
	s := strings.TrimRight(strings.TrimSpace(source), "/")
	s = strings.TrimSuffix(s, ".git")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	return s
}
