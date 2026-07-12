package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/registry"
	"github.com/spf13/cobra"
)

func newVerifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify <worktree-name>",
		Short: "Compare registry vs .env.local vs declared ranges and exit non-zero on drift",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			monorepoRoot, cfg, err := loadConfigAndRoot(cmd, "")
			if err != nil {
				return err
			}
			return runVerify(cmd.ErrOrStderr(), cmd.OutOrStdout(), cfg, monorepoRoot, args[0])
		},
	}
	return cmd
}

// runVerify is the rough equivalent of the prior in-house
// verify-worktree-isolation.sh hook — it asserts:
//
//   - the worktree directory exists,
//   - the registry has an entry for this name,
//   - every (engine, role) pair from `engines:` + every kind from
//     `ports:` is registered AND inside the declared range,
//   - every repository that declares `env_local` actually owns the
//     rendered `.env.local`.
//
// We don't run lsof against api / gateway here — those kinds have no
// plugin we can ask for the per-process check, so a "drifted" report
// against an unbooted api server would be misleading. The engine case
// goes through `bough status`.
func runVerify(stderr, stdout io.Writer, cfg *config.Config, monorepoRoot, name string) error {
	worktreePath := filepath.Join(worktreesDir(monorepoRoot), name)
	if _, err := os.Stat(worktreePath); err != nil {
		return fmt.Errorf("worktree dir absent: %s", worktreePath)
	}
	store := registry.NewStore(
		resolveRegistryPath(monorepoRoot, cfg.Registry.Path),
		cfg.Registry.BackupDir,
	)
	reg, err := store.Load()
	if err != nil {
		return err
	}
	entry, ok := reg[name]
	if !ok {
		return fmt.Errorf("no registry entry for %q", name)
	}
	var probs []string
	for _, eng := range cfg.Engines {
		// v0.4.0: allocateEngines only ever allocates/writes the "main"
		// role (single-port engines only; multi-port lands in Λ-7.4).
		// Demanding every declared PortRanges role here would report
		// permanent DRIFT against a key nothing will ever populate.
		mainRange, ok := eng.PortRanges["main"]
		if !ok {
			continue
		}
		key := eng.Kind + ".main"
		port, ok := entry[key]
		if !ok {
			probs = append(probs, fmt.Sprintf("registry missing %s entry", key))
			continue
		}
		if port < mainRange[0] || port > mainRange[1] {
			probs = append(probs, fmt.Sprintf("%s port %d outside range %v", key, port, mainRange))
		}
	}
	for kind, pr := range cfg.Ports {
		// Non-engine ports are also written under "<kind>.main"
		// (allocateNonEnginePorts, create.go) — Load() upgrades any
		// legacy bare key to that same dotted form, so the bare key
		// never exists post-v0.4.0.
		key := kind + ".main"
		port, ok := entry[key]
		if !ok {
			probs = append(probs, fmt.Sprintf("registry missing %s entry", key))
			continue
		}
		if port < pr.Range[0] || port > pr.Range[1] {
			probs = append(probs, fmt.Sprintf("%s port %d outside range %v", key, port, pr.Range))
		}
	}
	for _, repo := range cfg.Repositories {
		if len(repo.EnvLocal) == 0 {
			continue
		}
		envPath := filepath.Join(worktreePath, repo.Name, ".env.local")
		if _, err := os.Stat(envPath); err != nil {
			probs = append(probs, fmt.Sprintf("%s/.env.local missing", repo.Name))
		}
	}
	if len(probs) > 0 {
		fmt.Fprintln(stderr, "[verify] DRIFT detected:")
		for _, p := range probs {
			fmt.Fprintf(stderr, "  - %s\n", p)
		}
		return errors.New(strings.Join(probs, "; "))
	}
	fmt.Fprintf(stdout, "[verify] %s: PASS (%d port entries, all in range, env files present)\n", name, len(entry))
	return nil
}
