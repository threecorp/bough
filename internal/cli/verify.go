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
//   - every kind in `databases:` + `ports:` is registered AND inside the
//     declared range,
//   - every repository that declares `env_local` actually owns the
//     rendered `.env.local`.
//
// We don't run lsof against api / gateway here — those engines have no
// plugin we can ask for the per-process check, so a "drifted" report
// against an unbooted api server would be misleading. The mysql case
// goes through `bough status`.
func runVerify(stderr, stdout io.Writer, cfg *config.Config, monorepoRoot, name string) error {
	worktreePath := filepath.Join(monorepoRoot, ".worktrees", name)
	if _, err := os.Stat(worktreePath); err != nil {
		return fmt.Errorf("worktree dir absent: %s", worktreePath)
	}
	store := registry.NewStore(
		filepath.Join(monorepoRoot, cfg.Registry.Path),
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
	for _, db := range cfg.Databases {
		port, ok := entry[db.Kind]
		if !ok {
			probs = append(probs, fmt.Sprintf("registry missing %s entry", db.Kind))
			continue
		}
		if port < db.PortRange[0] || port > db.PortRange[1] {
			probs = append(probs, fmt.Sprintf("%s port %d outside range %v", db.Kind, port, db.PortRange))
		}
	}
	for kind, pr := range cfg.Ports {
		port, ok := entry[kind]
		if !ok {
			probs = append(probs, fmt.Sprintf("registry missing %s entry", kind))
			continue
		}
		if port < pr.Range[0] || port > pr.Range[1] {
			probs = append(probs, fmt.Sprintf("%s port %d outside range %v", kind, port, pr.Range))
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
