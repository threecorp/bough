package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/spf13/cobra"
)

// hookInput is the shape Claude Code's WorktreeCreate / WorktreeRemove
// hook contracts emit on stdin. The cwd field is set to the monorepo
// root that owns the .worktree-isolation.yaml; the bough host uses it
// to resolve every other path.
type hookInput struct {
	Name         string `json:"name"`
	Cwd          string `json:"cwd"`
	WorktreePath string `json:"worktree_path"`
}

// readHookStdin decodes hookInput from cmd.InOrStdin. The reader is
// indirected through cobra so unit tests can replace stdin with a
// bytes.Buffer.
func readHookStdin(cmd *cobra.Command) (hookInput, error) {
	var in hookInput
	raw, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return in, fmt.Errorf("read stdin: %w", err)
	}
	if len(raw) == 0 {
		return in, fmt.Errorf("--stdin-json was set but stdin was empty")
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, fmt.Errorf("parse stdin JSON: %w", err)
	}
	return in, nil
}

// resolveConfigPath answers "where does .worktree-isolation.yaml live?"
// in the standard order:
//
//  1. explicit --config FLAG
//  2. <monorepoRoot>/.worktree-isolation.yaml
//
// monorepoRoot is the absolute path the subcommand resolved from the
// hook input or os.Getwd().
func resolveConfigPath(cmd *cobra.Command, monorepoRoot string) string {
	if p, _ := cmd.Flags().GetString("config"); p != "" {
		return p
	}
	return filepath.Join(monorepoRoot, ".worktree-isolation.yaml")
}

// loadConfigAndRoot resolves the monorepo root, the config file, parses
// the YAML, and applies `config.MonorepoRoot` as a relative-path
// override on top of the caller-supplied root. The return is the
// absolute monorepo root + the parsed Config.
func loadConfigAndRoot(cmd *cobra.Command, cwdHint string) (string, *config.Config, error) {
	if cwdHint == "" {
		var err error
		cwdHint, err = os.Getwd()
		if err != nil {
			return "", nil, fmt.Errorf("getwd: %w", err)
		}
	}
	abs, err := filepath.Abs(cwdHint)
	if err != nil {
		return "", nil, fmt.Errorf("abs %s: %w", cwdHint, err)
	}
	cfg, err := config.Load(resolveConfigPath(cmd, abs))
	if err != nil {
		return "", nil, err
	}
	// `monorepo_root: "."` means "the directory holding the YAML";
	// anything else is resolved relative to that.
	if cfg.MonorepoRoot != "" && cfg.MonorepoRoot != "." {
		if filepath.IsAbs(cfg.MonorepoRoot) {
			abs = cfg.MonorepoRoot
		} else {
			abs = filepath.Join(abs, cfg.MonorepoRoot)
		}
	}
	return abs, cfg, nil
}

// rangeLen normalises a closed [low, high] port range into the half-open
// width the allocator wants. Returns 0 on malformed input — the config
// validator already rejects those at Load time, but a defensive guard
// here prevents an accidental infinite probe loop if the config layer
// is ever bypassed.
func rangeLen(r [2]int) int {
	if r[1] <= r[0] {
		return 0
	}
	return r[1] - r[0] + 1
}

// dbProviderRepo returns the YAML-declared db-provider repository when
// at least one database engine is configured. Cross-field invariants
// (exactly one provider when databases is non-empty) are enforced at
// config-load time, so this helper assumes a valid Config and returns
// nil otherwise.
func dbProviderRepo(cfg *config.Config) *config.Repository {
	if len(cfg.Databases) == 0 {
		return nil
	}
	for i := range cfg.Repositories {
		if cfg.Repositories[i].Role == "db-provider" {
			return &cfg.Repositories[i]
		}
	}
	return nil
}

// (Tilde expansion helpers live in internal/registry where the
// backup_dir field is actually consumed; the CLI never needs them
// directly so we leave that single source of truth in place.)
