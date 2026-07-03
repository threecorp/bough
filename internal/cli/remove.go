package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/gitwt"
	"github.com/ikeikeikeike/bough/internal/pluginhost"
	"github.com/ikeikeikeike/bough/internal/registry"
	engineapi "github.com/ikeikeikeike/bough/plugins/engine/api"

	"github.com/spf13/cobra"
)

func newRemoveCmd() *cobra.Command {
	var (
		name         string
		path         string
		stdinJSON    bool
		gracefulSecs int
	)
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Tear down a per-worktree environment created by `bough create`",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if stdinJSON {
				in, err := readHookStdin(cmd)
				if err != nil {
					return err
				}
				path = in.WorktreePath
				if path == "" {
					name = in.Name
				}
			}
			monorepoRoot, wtName, resolvedPath, err := resolveRemoveTarget(name, path)
			if err != nil {
				return err
			}
			abs, cfg, err := loadConfigAndRoot(cmd, monorepoRoot)
			if err != nil {
				return err
			}
			return runRemove(cmd.Context(), cmd.ErrOrStderr(), cfg, abs, wtName, resolvedPath, gracefulSecs)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "worktree name (when --path is not provided)")
	cmd.Flags().StringVar(&path, "path", "", "absolute worktree path (typical Claude Code stdin payload)")
	cmd.Flags().BoolVar(&stdinJSON, "stdin-json", false, "read {worktree_path} from stdin")
	cmd.Flags().IntVar(&gracefulSecs, "graceful-timeout", defaultRemoveGracefulSecs, "seconds to wait for plugin Down() before SIGKILL fallback (0 = let each engine plugin use its own tuned default)")
	return cmd
}

func runRemove(ctx context.Context, stderr io.Writer, cfg *config.Config, monorepoRoot, name, worktreePath string, gracefulSecs int) error {
	logf(stderr, "[bough] remove %s @ %s", name, worktreePath)

	store := registry.NewStore(
		resolveRegistryPath(monorepoRoot, cfg.Registry.Path),
		cfg.Registry.BackupDir,
	)
	reg, err := store.Load()
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}

	provider := engineProviderRepo(cfg)
	engineProviderWorktree := worktreePath
	if provider != nil {
		engineProviderWorktree = filepath.Join(worktreePath, provider.Name)
	}
	for _, eng := range cfg.Engines {
		port, _ := registry.Get(reg, name, eng.Kind+".main")
		if port <= 0 {
			logf(stderr, "[bough] %s: no registry entry, skipping plugin", eng.Kind)
			continue
		}
		prov, kill, err := pluginhost.Discover(eng.Kind)
		if err != nil {
			logf(stderr, "[bough] %s discover: %v", eng.Kind, err)
			continue
		}
		if err := prov.Down(ctx, &engineapi.DownReq{
			Ports:              []int{port},
			WorktreeRoot:       engineProviderWorktree,
			GracefulTimeoutSec: gracefulSecs,
		}); err != nil {
			logf(stderr, "[bough] %s Down: %v", eng.Kind, err)
		}
		if cfg.Teardown.RemoveDatadir {
			dataDir := filepath.Join(worktreePath, fmt.Sprintf(".local/%s-data", eng.Kind))
			if err := prov.Cleanup(ctx, dataDir, []int{port}); err != nil {
				logf(stderr, "[bough] %s Cleanup: %v", eng.Kind, err)
			}
		}
		kill()
	}

	runner := gitwt.NewRunner()
	for _, repo := range cfg.Repositories {
		repoSrc := filepath.Join(monorepoRoot, repo.Name)
		repoDst := filepath.Join(worktreePath, repo.Name)
		for _, line := range repo.PreRemove {
			logf(stderr, "[bough] %s pre_remove: %s", repo.Name, line)
			c := exec.CommandContext(ctx, "bash", "-c", line)
			c.Dir = repoDst
			c.Stdout = stderr
			c.Stderr = stderr
			_ = c.Run()
		}
		if _, err := os.Stat(repoSrc); err != nil {
			continue
		}
		if err := runner.Remove(ctx, repoSrc, repoDst, true); err != nil {
			logf(stderr, "[bough] %s worktree remove: %v", repo.Name, err)
		}
		if cfg.Teardown.RemoveBranch {
			if err := runner.DeleteBranch(ctx, repoSrc, name); err != nil {
				logf(stderr, "[bough] %s branch -D: %v", repo.Name, err)
			}
		}
	}

	if err := os.RemoveAll(worktreePath); err != nil {
		logf(stderr, "[bough] rm -rf %s: %v", worktreePath, err)
	}

	registry.Delete(reg, name)
	if err := store.Save(reg, "cleanup"); err != nil {
		return fmt.Errorf("save registry: %w", err)
	}
	logf(stderr, "[bough] remove %s: complete", name)
	return nil
}
