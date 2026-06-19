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

	// v0.5 instinct subsystem (opt-in; disabled by default for full
	// v0.4 compatibility). See InstinctConfig docs below.
	Instinct        InstinctConfig     `yaml:"instinct"`
	MemoryBackends  []MemoryBackendCfg `yaml:"memory_backends" validate:"dive"`
	Export          ExportConfig       `yaml:"export"`
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

// InstinctConfig is the v0.5 opt-in instinct subsystem. When
// `enabled: false` (the default) the host behaves exactly as it did
// under v0.4: no observer is wired up, no memory backends are
// discovered, and the `bough instinct` / `bough memory` CLI
// subcommands print a "subsystem disabled" notice. This makes v0.5
// release a pure additive bump for existing v0.4 monorepos.
//
// When `enabled: true`, the host stands up the coordinator
// (internal/instinct/), the redaction + poisoning_guard + decay
// pipeline, the configured backends in MemoryBackends, and the
// observer pipeline (stdin ingest as primary, optional opt-in beta
// file watch). See docs/INSTINCTS.md for the per-monorepo design
// choices users make here.
type InstinctConfig struct {
	Enabled               bool                  `yaml:"enabled"`
	DefaultMemoryBackend  string                `yaml:"default_memory_backend"`
	DefaultInstinctMinter string                `yaml:"default_instinct_minter"`
	// FallbackOnError tells the coordinator whether to silently
	// degrade to the SQLite reference-fallback backend when the
	// primary (external) backend reports an error, or to fail the
	// operation. Production teams using mem0 / Graphiti typically
	// want `true` so a transient network blip does not block a CI
	// run; users debugging an external backend may want `false`.
	FallbackOnError       bool                   `yaml:"fallback_on_error"`
	Scopes                InstinctScopes         `yaml:"scopes"`
	Mint                  InstinctMint           `yaml:"mint"`
	Retrieve              InstinctRetrieve       `yaml:"retrieve"`
	Confidence            InstinctConfidence     `yaml:"confidence"`
	PoisoningGuard        InstinctPoisoningGuard `yaml:"poisoning_guard"`
	Observer              InstinctObserver       `yaml:"observer"`
	PluginSecurity        InstinctPluginSecurity `yaml:"plugin_security"`
}

// InstinctScopes toggles which of the three scope tiers the
// coordinator stores into. All three default to true; turning one
// off (e.g. global=false on a personal monorepo) tells the
// coordinator to short-circuit promotion attempts and to refuse
// `bough instinct promote <id> --to global`.
type InstinctScopes struct {
	Worktree bool `yaml:"worktree"`
	Repo     bool `yaml:"repo"`
	Global   bool `yaml:"global"`
}

// InstinctMint controls how raw observations are turned into
// candidate instincts. Mode="hybrid" (the default) emits each
// minter output as `state:"candidate"` and requires a `bough
// instinct approve <id>` before it goes active; mode="auto-
// candidate" skips the approval gate (dangerous, off by default);
// mode="manual" disables auto-minting entirely so only `bough
// instinct mint --rule '...'` produces rows; mode="off" disables
// minting entirely (useful when the user wants only the persistence
// half of the subsystem).
//
// Sources lists which observer-emitted TraceBundle kinds the minter
// will accept. Note that `session_log` is intentionally NOT in the
// v0.5 default list: the file watch observer is opt-in beta because
// of fsnotify cross-platform fragility (macOS FSEvents vs Linux
// inotify, log rotation, truncate). Production users should pipe
// CI / make output through `bough instinct ingest --stdin` instead.
type InstinctMint struct {
	Mode            string            `yaml:"mode" validate:"omitempty,oneof=off manual auto-candidate hybrid"`
	RequireApproval bool              `yaml:"require_approval"`
	Sources         []string          `yaml:"sources"`
	Redaction       InstinctRedaction `yaml:"redaction"`
}

// InstinctRedaction sanitises raw TraceBundle content before any
// minter sees it. Enabled by default; users explicitly opting out
// should understand that PII / secrets observed in a session log
// will land in the SQLite store verbatim.
type InstinctRedaction struct {
	Enabled     bool     `yaml:"enabled"`
	PIIPatterns []string `yaml:"pii_patterns"`
}

// InstinctRetrieve caps query results so accumulated memory does
// not blow Claude's context window. Both MaxResults and MaxTokens
// are hard limits: the validator forces a sane default if the user
// sets them to zero. HybridSearch=false on v0.5 means the SQLite
// backend uses FTS5 only; v0.6 will gate dense-vector reranking
// behind this flag.
type InstinctRetrieve struct {
	MaxResults    int     `yaml:"max_results"`
	MaxTokens     int     `yaml:"max_tokens"`
	MinConfidence float64 `yaml:"min_confidence"`
	HybridSearch  bool    `yaml:"hybrid_search"`
}

// InstinctConfidence is the source-aware initial-confidence and
// decay policy. Sources maps a TraceBundle.Source to the ceiling
// confidence a minter is allowed to emit: explicit user feedback
// scores higher than LLM-only inference because the host trusts
// the former more. ReinforceDelta is added each time a stored
// instinct's dedupe_key matches an incoming Store. DecayAfterDays
// is the soft TTL the coordinator's decay_scheduler uses to bump
// `last_hit_at`-stale rows toward state:"archived".
type InstinctConfidence struct {
	Sources        map[string]float64 `yaml:"sources"`
	ReinforceDelta float64            `yaml:"reinforce_delta"`
	DecayAfterDays int                `yaml:"decay_after_days"`
}

// InstinctPoisoningGuard backstops the auto-candidate / hybrid mint
// modes. MaxActivePerScope is the soft cap on `state:"active"`
// rows the coordinator allows per scope tier before it starts
// auto-archiving the lowest-confidence rows. CandidateTTLDays is
// how long an un-approved candidate sits before the coordinator
// auto-forgets it. DedupeStrategy picks the hash function (only
// "sha256" is implemented in v0.5).
type InstinctPoisoningGuard struct {
	MaxActivePerScope int    `yaml:"max_active_per_scope"`
	CandidateTTLDays  int    `yaml:"candidate_ttl_days"`
	DedupeStrategy    string `yaml:"dedupe_strategy" validate:"omitempty,oneof=sha256"`
}

// InstinctObserver controls the observer pipeline. v0.5 ships two
// observers: a stdin ingest path (always available, primary) and a
// Claude Code `.jsonl` file watch (opt-in beta, off by default).
// FileWatch.Enabled gates whether the host even tries to set up
// fsnotify; users on Linux who want the experimental path can set
// it true, but the docs urge piping the relevant CI / make output
// through `bough instinct ingest --stdin` instead.
type InstinctObserver struct {
	FileWatch InstinctFileWatch `yaml:"file_watch"`
}

// InstinctFileWatch is the opt-in beta `.jsonl` tail config. The
// Linux-vs-macOS event semantics differ enough that we treat
// rotation and truncate as required handling rather than optional.
// Debounce ms gates how aggressively the observer batches events;
// 0 disables debouncing.
type InstinctFileWatch struct {
	Enabled            bool   `yaml:"enabled"`
	Stability          string `yaml:"stability" validate:"omitempty,oneof=stable preview beta"`
	JSONLPathTemplate  string `yaml:"jsonl_path_template"`
	RotationHandling   bool   `yaml:"rotation_handling"`
	TruncateHandling   bool   `yaml:"truncate_handling"`
	DebounceMs         int    `yaml:"debounce_ms"`
}

// InstinctPluginSecurity governs third-party plugin trust. v0.5
// ships `require_signed: false` (warn-only) because the v0.5 plugin
// ecosystem is mostly the bundled SQLite reference-fallback;
// v0.6 will gain an enforce option once mem0 / Graphiti plugins
// ship signed. UntrustedWarning=true tells the host CLI to print
// a "third-party plugin = untrusted code" banner whenever a non-
// allowlisted plugin is discovered.
type InstinctPluginSecurity struct {
	RequireSigned     bool     `yaml:"require_signed"`
	Allowlist         []string `yaml:"allowlist"`
	UntrustedWarning  bool     `yaml:"untrusted_warning"`
}

// MemoryBackendCfg declares one persistent memory backend the
// coordinator should discover and route store/query calls through.
// v0.5 ships only `kind: sqlite` (the reference-fallback); v0.6+
// will add `kind: mem0` / `kind: graphiti` as official optional
// plugins. The host treats Path / EventsLog / MirrorDir / FTS / WAL
// / BusyTimeoutMs / Vector as plugin-specific tuning that the
// memory plugin reads via the gRPC Capabilities / Health pair.
//
// Role="reference-fallback" is the canonical role for the SQLite
// backend — calling it just `"reference"` invites the misreading
// that bough is competing with mem0 / Graphiti. v0.6+ external
// backends declare role="external".
type MemoryBackendCfg struct {
	Kind          string                 `yaml:"kind" validate:"required"`
	Role          string                 `yaml:"role" validate:"required,oneof=reference-fallback external"`
	Path          string                 `yaml:"path"`
	EventsLog     string                 `yaml:"events_log"`
	MirrorDir     string                 `yaml:"mirror_dir"`
	FTS           bool                   `yaml:"fts"`
	WAL           bool                   `yaml:"wal"`
	BusyTimeoutMs int                    `yaml:"busy_timeout_ms"`
	Vector        MemoryBackendVector    `yaml:"vector"`
	Endpoint      string                 `yaml:"endpoint"`            // v0.6+ external
	APIKeyEnv     string                 `yaml:"api_key_env"`         // v0.6+ external
	Fallback      string                 `yaml:"fallback"`            // v0.6+ chain
	Extras        map[string]string      `yaml:"extras"`
}

// MemoryBackendVector toggles dense vector indexing inside a memory
// backend. v0.5 plugins must accept this field but should treat
// `enabled: true` as a no-op (the SQLite reference-fallback does
// not ship a vector index). v0.6+ mem0 / Graphiti plugins will
// honour it.
type MemoryBackendVector struct {
	Enabled bool   `yaml:"enabled"`
	Model   string `yaml:"model"`
}

// ExportConfig governs `bough instinct export` defaults. v0.5
// supports `yaml` and `jsonl`; v0.6 adds `claude-skills`,
// `agent-skill`, and `mcp`. OutputDir is the directory the exporter
// writes into when the user does not pass `--out`.
type ExportConfig struct {
	Formats   []string `yaml:"formats"`
	OutputDir string   `yaml:"output_dir"`
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
	c.applyInstinctDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("validate %s: %w", pathHint, err)
	}
	return c, nil
}

// applyInstinctDefaults fills sane defaults for the v0.5 instinct
// subsystem so a user who toggles `instinct.enabled: true` does not
// have to spell out every nested field. The defaults match the
// docs/INSTINCTS.md "default profile" so a fresh `.bough.yaml`
// snippet (just `instinct: { enabled: true }`) produces a working
// subsystem.
//
// The hard-limit fallbacks for retrieve.max_results and
// retrieve.max_tokens are required: an unconfigured backend that
// returns unbounded results would blow Claude's context window the
// first time `bough instinct query` runs. Validator-level defaults
// guard against the user forgetting to set them.
func (c *Config) applyInstinctDefaults() {
	if c.Instinct.DefaultMemoryBackend == "" {
		c.Instinct.DefaultMemoryBackend = "sqlite"
	}
	if c.Instinct.DefaultInstinctMinter == "" {
		c.Instinct.DefaultInstinctMinter = "builtin"
	}

	if c.Instinct.Mint.Mode == "" {
		c.Instinct.Mint.Mode = "hybrid"
	}
	if len(c.Instinct.Mint.Sources) == 0 {
		// session_log is intentionally NOT default-enabled: the file
		// watch observer is opt-in beta. Production users pipe CI /
		// make output through `bough instinct ingest --stdin`.
		c.Instinct.Mint.Sources = []string{"stdin", "test_failure", "lint_output", "commit_message", "post_create_hook"}
	}
	if len(c.Instinct.Mint.Redaction.PIIPatterns) == 0 {
		c.Instinct.Mint.Redaction.PIIPatterns = []string{"email", "api_key", "token", "password", "aws_secret"}
	}

	// Retrieve hard-limit fallbacks (round 3 AI #2 + AI #4). The
	// host's budget aggregator trusts these caps to bound how much
	// memory ever lands in a Claude prompt.
	if c.Instinct.Retrieve.MaxResults <= 0 {
		c.Instinct.Retrieve.MaxResults = 12
	}
	if c.Instinct.Retrieve.MaxTokens <= 0 {
		c.Instinct.Retrieve.MaxTokens = 4000
	}
	if c.Instinct.Retrieve.MinConfidence <= 0 {
		c.Instinct.Retrieve.MinConfidence = 0.55
	}

	// Source-aware confidence ceilings (round 1 AI #4 + round 2 AI #1).
	if c.Instinct.Confidence.Sources == nil {
		c.Instinct.Confidence.Sources = map[string]float64{
			"explicit_user_feedback": 0.75,
			"test_failure":           0.60,
			"session_summary":        0.45,
			"llm_only":               0.30,
		}
	}
	if c.Instinct.Confidence.ReinforceDelta <= 0 {
		c.Instinct.Confidence.ReinforceDelta = 0.10
	}
	if c.Instinct.Confidence.DecayAfterDays <= 0 {
		c.Instinct.Confidence.DecayAfterDays = 30
	}

	if c.Instinct.PoisoningGuard.MaxActivePerScope <= 0 {
		c.Instinct.PoisoningGuard.MaxActivePerScope = 200
	}
	if c.Instinct.PoisoningGuard.CandidateTTLDays <= 0 {
		c.Instinct.PoisoningGuard.CandidateTTLDays = 14
	}
	if c.Instinct.PoisoningGuard.DedupeStrategy == "" {
		c.Instinct.PoisoningGuard.DedupeStrategy = "sha256"
	}

	if !c.Instinct.PluginSecurity.UntrustedWarning {
		c.Instinct.PluginSecurity.UntrustedWarning = true
	}

	if len(c.Export.Formats) == 0 {
		c.Export.Formats = []string{"yaml", "jsonl"}
	}
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
