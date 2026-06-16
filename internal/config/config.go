// Package config defines the on-disk YAML schema (`.worktree-isolation.yaml`)
// that drives the bough orchestrator and the loader / validator that the host
// CLI consumes.
//
// The schema is fully declarative — a monorepo declares its sub-repos,
// the database engines it wants per-worktree, and the per-kind port
// ranges. The host iterates over Config.Repositories to drive
// `git worktree add`, `direnv allow`, `.env.local` rendering, and
// post-create hooks. It iterates over Config.Databases to spawn the
// matching `bough-plugin-<kind>` gRPC plugin and call its lifecycle
// methods. Neither host nor plugin owns a copy of the per-monorepo
// policy — it lives entirely in this YAML.
//
// Validation is performed via github.com/go-playground/validator/v10 struct
// tags. Custom semantic rules (e.g. "exactly one db-provider repo when at
// least one Databases entry is set") live in validate.go.
package config

import (
	"fmt"
	"os"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

// Config is the root of `.worktree-isolation.yaml`.
//
// `schema_version` is required so the loader can refuse forward-incompatible
// files instead of silently mapping unknown fields to zero values; bumping
// the major schema version is therefore a breaking change.
type Config struct {
	SchemaVersion int                  `yaml:"schema_version" validate:"required,eq=1"`
	MonorepoRoot  string               `yaml:"monorepo_root" validate:"required"`
	Repositories  []Repository         `yaml:"repositories" validate:"required,min=1,dive"`
	Databases     []Database           `yaml:"databases" validate:"dive"`
	Ports         map[string]PortRange `yaml:"ports" validate:"dive"`
	Registry      RegistryConfig       `yaml:"registry" validate:"required"`
	Teardown      TeardownConfig       `yaml:"teardown"`
	MCP           MCPConfig            `yaml:"mcp"`
}

// Repository declares one git sub-repo that hangs off `.worktrees/<name>/`.
//
// Role values:
//   - "" (empty): the worktree is created and direnv-allowed but receives no
//     `.env.local` injection beyond `EnvLocal` (used for proto / build-tool
//     repos that have no port dependency).
//   - "db-provider": the worktree owns the per-worktree database datadir and
//     is the cwd from which the bough host issues `nix run --impure '.#mysql'
//     -- up`. Exactly one repository per Config carries this role when at
//     least one `databases:` entry is present.
type Repository struct {
	Name           string            `yaml:"name" validate:"required"`
	BranchStrategy string            `yaml:"branch_strategy" validate:"required"`
	Direnv         bool              `yaml:"direnv"`
	Role           string            `yaml:"role" validate:"omitempty,oneof=db-provider"`
	Symlinks       []SymlinkSpec     `yaml:"symlinks" validate:"dive"`
	EnvLocal       map[string]string `yaml:"env_local"`
	PostCreate     []string          `yaml:"post_create"`
	PreRemove      []string          `yaml:"pre_remove"`
}

// Database picks a `bough-plugin-<Kind>` binary (Hashicorp go-plugin gRPC)
// and supplies its per-instance parameters.
//
// `port_range` overrides the plugin's default — typically left unset unless
// the monorepo wants to coexist with another orchestrated environment whose
// ranges would otherwise overlap.
type Database struct {
	Kind             string   `yaml:"kind" validate:"required"`
	Version          string   `yaml:"version" validate:"required"`
	PortRange        [2]int   `yaml:"port_range" validate:"required"`
	SocketDir        string   `yaml:"socket_dir"`
	InitialDatabases []string `yaml:"initial_databases"`
	// Backend selects the lifecycle implementation inside the plugin.
	// Allowed values: "nix" (default, current bough v0.1.x) or "docker"
	// (v0.2+, bind-mounts datadir into the engine's official Docker
	// image). Empty = plugin default. v0.3 will add an "auto" mode that
	// picks based on host runtime detection.
	Backend string `yaml:"backend" validate:"omitempty,oneof=nix docker"`
	// ReadyTimeoutSec caps how long the host waits for the plugin's
	// ReadyCheck loop to report ready. Zero means use the plugin's own
	// default (600s — generous enough for a nix cold path that resolves
	// flake inputs + builds the engine derivation on a cold store).
	ReadyTimeoutSec int               `yaml:"ready_timeout_sec" validate:"omitempty,min=1"`
	Extras          map[string]string `yaml:"extras"`
}

// PortRange covers all non-DB port kinds (api / gateway / view / ...).
// DB port ranges live in `Database.PortRange` because they are owned by the
// plugin, not the host.
type PortRange struct {
	Range [2]int `yaml:"range" validate:"required"`
}

// RegistryConfig points at the `.worktree-ports.json` atomic registry that
// holds the deterministic port allocation per branch.
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

// MCPConfig wires `~/.claude.json` projects-entry bootstrap so a Claude Code
// session opened inside a new worktree sees the same MCP servers as the
// parent monorepo. Disabled by default — opt in by setting `enabled: true`.
type MCPConfig struct {
	Enabled       bool   `yaml:"enabled"`
	SourceOfTruth string `yaml:"source_of_truth"`
}

// SymlinkSpec declares one symlink to drop into the worktree root after
// `git worktree add` (typically used to re-expose CLAUDE.md so edits in the
// worktree reflect back to the monorepo root copy).
type SymlinkSpec struct {
	Target string `yaml:"target" validate:"required"`
	Link   string `yaml:"link" validate:"required"`
}

// Load reads, parses, and validates `.worktree-isolation.yaml`.
//
// `strict` decoding is enabled so a typo in a field name (e.g. `repositries:`
// instead of `repositories:`) raises a hard error instead of silently
// dropping the entry — config drift is otherwise extremely hard to debug
// once a worktree is spawned against a half-applied policy.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(newByteReader(raw))
	dec.KnownFields(true) // strict
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &c, nil
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
