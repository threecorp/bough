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
	"github.com/ikeikeikeike/bough/internal/termio"
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
	// Same one-mutex-per-fd routing as runCreate: the plugin Down/Cleanup
	// calls below spawn hclog writers targeting termio.Stderr, so remove's
	// own logf lines must share that mutex rather than race it on fd 2.
	stderr = termio.Wrap(stderr)
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

	// Raw fd for hook children — see runPostCreateHooks: an exec.Cmd
	// handed the SyncWriter gets a pipe + copy goroutine whose EOF a
	// backgrounded grandchild can hold open forever.
	hookOut := termio.ExecWriter(stderr)
	runner := gitwt.NewRunner()
	for _, repo := range cfg.Repositories {
		repoDst := filepath.Join(worktreePath, repo.Name)
		// Prefer the source recorded in the worktree's own gitlink so
		// remove targets the exact repo create registered this worktree
		// against, even if resolveRepoSrc would now resolve elsewhere
		// (post-migration drift). Fall back to resolveRepoSrc when the
		// worktree copy is already gone / not a linked worktree.
		repoSrc, ok := worktreeSourceRepo(repoDst)
		if !ok {
			repoSrc = resolveRepoSrc(monorepoRoot, repo.Name)
		}
		for _, line := range repo.PreRemove {
			logf(stderr, "[bough] %s pre_remove: %s", repo.Name, line)
			c := exec.CommandContext(ctx, "bash", "-c", line)
			c.Dir = repoDst
			c.Stdout = hookOut
			c.Stderr = hookOut
			if err := c.Run(); err != nil {
				logf(stderr, "[bough] %s pre_remove: %v", repo.Name, err)
			}
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
