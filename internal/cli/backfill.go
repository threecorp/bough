package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/registry"
	"github.com/spf13/cobra"
)

func newBackfillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Register pre-existing .worktrees/* into .worktree-ports.json without re-launching anything",
		Long: `backfill walks .worktrees/* looking for directories that resemble bough
worktrees and registers them in .worktree-ports.json so subsequent
allocations don't accidentally re-use the same port. The mysqld is not
restarted — pre-existing .env.local files keep their port assignments.

Use this after upgrading to bough from a hand-rolled hook, or after a
registry corruption recovered from ~/.claude/backups/. Subsequent
` + "`bough create <name>`" + ` calls remain deterministic against the freshly-
written registry.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			monorepoRoot, cfg, err := loadConfigAndRoot(cmd, "")
			if err != nil {
				return err
			}
			return runBackfill(cmd.ErrOrStderr(), cfg, monorepoRoot)
		},
	}
	return cmd
}

// runBackfill walks .worktrees/*/ and adds any unregistered name to the
// registry with an empty entry. The follow-up `bough create <name>`
// is what actually allocates ports — backfill alone is the "stop
// allocator from re-issuing this name's slot" pass.
func runBackfill(stderr io.Writer, cfg *config.Config, monorepoRoot string) error {
	root := filepath.Join(monorepoRoot, ".worktrees")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(stderr, "[backfill] .worktrees/ does not exist — nothing to do")
			return nil
		}
		return fmt.Errorf("read .worktrees/: %w", err)
	}
	store := registry.NewStore(
		filepath.Join(monorepoRoot, cfg.Registry.Path),
		cfg.Registry.BackupDir,
	)
	reg, err := store.Load()
	if err != nil {
		return err
	}
	added := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if _, exists := reg[name]; exists {
			continue
		}
		reg[name] = map[string]int{}
		added++
	}
	if added == 0 {
		fmt.Fprintln(stderr, "[backfill] all worktree dirs already registered — no changes")
		return nil
	}
	if err := store.Save(reg, "backfill"); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "[backfill] registered %d worktree dir(s) (run `bough create <name>` to allocate ports)\n", added)
	return nil
}
