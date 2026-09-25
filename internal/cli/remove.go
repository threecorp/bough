package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

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
	type stopped struct {
		kind string
		port int
		prov engineapi.EngineProvider
		kill func()
	}
	var downed []stopped
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
		downed = append(downed, stopped{eng.Kind, port, prov, kill})
	}
	killAll := func() {
		for _, d := range downed {
			d.kill()
		}
	}

	// Raw fd for hook children — see runPostCreateHooks: an exec.Cmd
	// handed the SyncWriter gets a pipe + copy goroutine whose EOF a
	// backgrounded grandchild can hold open forever.
	hookOut := termio.ExecWriter(stderr)
	// pre_remove runs before the port check below, because it is where an
	// operator stops an engine bough does not manage.
	for _, repo := range cfg.Repositories {
		repoDst := filepath.Join(worktreePath, repo.Name)
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
	}

	// Down succeeds when the plugin finds nothing of its own to stop, so a
	// process bough did not start (a Nix-backed engine from v0.26.0 or
	// earlier, say) can still be serving the datadir. Every port the
	// registry holds for this worktree is checked — an engine since dropped
	// from .bough.yaml included — except the non-engine `ports:` ones, which
	// an app server may legitimately still hold.
	busy, err := portsStillServing(ctx, guardedPorts(reg[name], cfg), portReleaseWait)
	if err != nil || len(busy) > 0 {
		killAll()
		if err != nil {
			return fmt.Errorf("remove %s: could not confirm the engine ports are free (%w); "+
				"no datadir, worktree or registry entry was deleted", name, err)
		}
		return fmt.Errorf("remove %s: port(s) %v still accept connections after the engines were stopped; "+
			"a process bough did not start still owns them (find it with `lsof -nP -iTCP:%d -sTCP:LISTEN`). "+
			"Stop it and run remove again; no datadir, worktree or registry entry was deleted", name, busy, busy[0])
	}

	for _, d := range downed {
		if cfg.Teardown.RemoveDatadir {
			dataDir := filepath.Join(worktreePath, fmt.Sprintf(".local/%s-data", d.kind))
			if err := d.prov.Cleanup(ctx, dataDir, []int{d.port}); err != nil {
				logf(stderr, "[bough] %s Cleanup: %v", d.kind, err)
			}
		}
	}
	killAll()

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

	// The container is itself a detached worktree of the monorepo root
	// whenever that root is a git repo (see materializeWorktreeRoot), so a
	// bare rm -rf would leave the root repo holding an admin entry for a
	// directory that no longer exists — `git worktree list` keeps naming
	// it, and the next create of the same name has to prune before it can
	// add. Runner.Remove already degrades to rm + prune, which is exactly
	// the right fallback here and also covers legacy plain containers.
	if runner.SelfResolvingWorkTree(ctx, worktreePath) {
		if err := runner.Remove(ctx, monorepoRoot, worktreePath, true); err != nil {
			logf(stderr, "[bough] worktree remove %s: %v", worktreePath, err)
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

// portReleaseWait is how long remove lets a stopped engine release its
// port before treating the port as owned by something else.
const portReleaseWait = 5 * time.Second

// guardedPorts lists the ports remove must find closed before deleting:
// every port the registry holds for the worktree except those allocated
// for the non-engine `ports:` section.
func guardedPorts(entry map[string]int, cfg *config.Config) []int {
	engineKinds := make(map[string]bool, len(cfg.Engines))
	for _, eng := range cfg.Engines {
		engineKinds[eng.Kind] = true
	}
	var out []int
	for key, port := range entry {
		kind, _, _ := strings.Cut(key, ".")
		if _, isAppPort := cfg.Ports[kind]; isAppPort && !engineKinds[kind] {
			continue
		}
		if port > 0 {
			out = append(out, port)
		}
	}
	sort.Ints(out)
	return out
}

// portsStillServing returns the ports that still accept a TCP connection on
// either loopback once wait has passed. A connect is used rather than a
// trial bind: on macOS a bind to 127.0.0.1 succeeds beside a wildcard
// listener. A cancelled context is an error, never "all clear".
func portsStillServing(ctx context.Context, ports []int, wait time.Duration) ([]int, error) {
	deadline := time.Now().Add(wait)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var busy []int
		for _, port := range ports {
			if answers(ctx, "127.0.0.1", port) || answers(ctx, "::1", port) {
				busy = append(busy, port)
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(busy) == 0 || time.Now().After(deadline) {
			return busy, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// answers reports whether host:port may still be served. Only a refused
// connection, or a loopback the machine does not have, counts as closed; a
// timeout or any other failure could hide a live engine, so it counts as
// serving and remove waits, then refuses.
func answers(ctx context.Context, host string, port int) bool {
	var d net.Dialer
	dctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	conn, err := d.DialContext(dctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err == nil {
		_ = conn.Close()
		return true
	}
	for _, closed := range []error{syscall.ECONNREFUSED, syscall.EADDRNOTAVAIL, syscall.ENETUNREACH, syscall.ENETDOWN, syscall.EHOSTUNREACH, syscall.EAFNOSUPPORT} {
		if errors.Is(err, closed) {
			return false
		}
	}
	return true
}
