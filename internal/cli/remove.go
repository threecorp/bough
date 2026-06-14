package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/gitwt"
	"github.com/ikeikeikeike/bough/internal/pluginhost"
	"github.com/ikeikeikeike/bough/internal/registry"
	api "github.com/ikeikeikeike/bough/plugins/db/api"
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
				// If only `name` was supplied by the hook, fall through to
				// the --name branch.
				if path == "" {
					name = in.Name
				}
			}
			var (
				monorepoRoot string
				wtName       string
			)
			switch {
			case path != "":
				wtName = filepath.Base(path)
				monorepoRoot = filepath.Dir(filepath.Dir(path)) // <root>/.worktrees/<name> → <root>
			case name != "":
				cwd, _ := os.Getwd()
				monorepoRoot = cwd
				path = filepath.Join(cwd, ".worktrees", name)
				wtName = name
			default:
				return errors.New("--name or --path is required (or pass --stdin-json with a worktree_path payload)")
			}
			abs, cfg, err := loadConfigAndRoot(cmd, monorepoRoot)
			if err != nil {
				return err
			}
			return runRemove(cmd.Context(), cmd.ErrOrStderr(), cfg, abs, wtName, path, gracefulSecs)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "worktree name (when --path is not provided)")
	cmd.Flags().StringVar(&path, "path", "", "absolute worktree path (typical Claude Code stdin payload)")
	cmd.Flags().BoolVar(&stdinJSON, "stdin-json", false, "read {worktree_path} from stdin")
	cmd.Flags().IntVar(&gracefulSecs, "graceful-timeout", 10, "seconds to wait for plugin Down() before SIGKILL fallback")
	return cmd
}

// runRemove tears down in roughly the inverse order of create. We are
// deliberately tolerant — every step that can succeed independently
// does so, so a partially broken state (mysqld already dead, branch
// already deleted, etc.) still converges on "no record of this
// worktree".
func runRemove(ctx context.Context, stderr io.Writer, cfg *config.Config, monorepoRoot, name, worktreePath string, gracefulSecs int) error {
	logf(stderr, "[bough] remove %s @ %s", name, worktreePath)

	// 1. Registry first — we want to know the ports we allocated even
	// if the worktree dir is half-deleted.
	store := registry.NewStore(
		filepath.Join(monorepoRoot, cfg.Registry.Path),
		cfg.Registry.BackupDir,
	)
	reg, err := store.Load()
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}

	// 2. Down + Cleanup every DB plugin. Discover may fail for kinds
	// the operator never installed; we log + continue.
	provider := dbProviderRepo(cfg)
	dbProviderWorktree := worktreePath
	if provider != nil {
		dbProviderWorktree = filepath.Join(worktreePath, provider.Name)
	}
	for _, dbCfg := range cfg.Databases {
		port, _ := registry.Get(reg, name, dbCfg.Kind)
		if port <= 0 {
			logf(stderr, "[bough] %s: no registry entry, skipping plugin", dbCfg.Kind)
			continue
		}
		prov, kill, err := pluginhost.Discover(dbCfg.Kind)
		if err != nil {
			logf(stderr, "[bough] %s discover: %v", dbCfg.Kind, err)
			continue
		}
		if err := prov.Down(ctx, api.DownReq{
			Port: port, WorktreeRoot: dbProviderWorktree, GracefulTimeoutSec: gracefulSecs,
		}); err != nil {
			logf(stderr, "[bough] %s Down: %v", dbCfg.Kind, err)
		}
		if cfg.Teardown.RemoveDatadir {
			dataDir := filepath.Join(worktreePath, fmt.Sprintf(".local/%s-data", dbCfg.Kind))
			if err := prov.Cleanup(ctx, dataDir, port); err != nil {
				logf(stderr, "[bough] %s Cleanup: %v", dbCfg.Kind, err)
			}
		}
		kill()
	}

	// 3. pre_remove hooks then git worktree remove + branch -D.
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

	// 4. Remove the worktree root dir.
	if err := os.RemoveAll(worktreePath); err != nil {
		logf(stderr, "[bough] rm -rf %s: %v", worktreePath, err)
	}

	// 5. Drop the registry entry.
	registry.Delete(reg, name)
	if err := store.Save(reg, "cleanup"); err != nil {
		return fmt.Errorf("save registry: %w", err)
	}
	logf(stderr, "[bough] remove %s: complete", name)
	return nil
}
