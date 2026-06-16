package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ikeikeikeike/bough/internal/allocator"
	"github.com/ikeikeikeike/bough/internal/backend"
	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/envwriter"
	"github.com/ikeikeikeike/bough/internal/gitwt"
	"github.com/ikeikeikeike/bough/internal/pluginhost"
	"github.com/ikeikeikeike/bough/internal/registry"
	api "github.com/ikeikeikeike/bough/plugins/db/api"
	"github.com/spf13/cobra"
)

func newCreateCmd() *cobra.Command {
	var (
		name      string
		cwd       string
		stdinJSON bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Bootstrap a per-worktree isolated environment",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if stdinJSON {
				in, err := readHookStdin(cmd)
				if err != nil {
					return err
				}
				name, cwd = in.Name, in.Cwd
			}
			if name == "" {
				return errors.New("--name is required (or pass --stdin-json with a {name, cwd} payload)")
			}
			monorepoRoot, cfg, err := loadConfigAndRoot(cmd, cwd)
			if err != nil {
				return err
			}
			return runCreate(cmd.Context(), cmd.ErrOrStderr(), cmd.OutOrStdout(), cfg, monorepoRoot, name)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "worktree name (mutually exclusive with --stdin-json)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "monorepo root (defaults to current working dir; overridden by --stdin-json)")
	cmd.Flags().BoolVar(&stdinJSON, "stdin-json", false, "read {name, cwd} from stdin in Claude Code WorktreeCreate hook format")
	return cmd
}

// dbInstance holds the per-database side-effects we need to remember
// between the spawn-pass and the env-render pass. The connection cleanup
// closure runs at the end of runCreate regardless of partial-success —
// keeping the gRPC subprocess alive would leak after the host returns.
type dbInstance struct {
	cfg     config.Database
	port    int
	envVars map[string]string
	kill    func()
}

// runCreate is the ordered choreography of allocator → registry →
// gitwt → pluginhost → envwriter → post_create. Errors are progressive:
// per-repo failures log to stderr and continue (the host typically
// converges across two-or-three `bough create` retries), but registry /
// plugin failures abort because they leave the operator in an
// inconsistent state.
func runCreate(ctx context.Context, stderr, stdout interface{ Write([]byte) (int, error) }, cfg *config.Config, monorepoRoot, name string) error {
	logf(stderr, "[bough] create %s @ %s", name, monorepoRoot)
	worktreeRoot := filepath.Join(monorepoRoot, ".worktrees", name)
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir worktree root: %w", err)
	}

	// 1. Registry: load, allocate, save in one mutation block. The
	// registry is the single source of truth for "which ports does this
	// worktree own"; the allocator only ever sees the map snapshot.
	store := registry.NewStore(
		filepath.Join(monorepoRoot, cfg.Registry.Path),
		cfg.Registry.BackupDir,
	)
	reg, err := store.Load()
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}
	dbPorts, err := allocateRegistry(reg, cfg, name)
	if err != nil {
		return err
	}
	portsCtx, err := allocateNonDBPorts(reg, cfg, name)
	if err != nil {
		return err
	}
	if err := store.Save(reg, "allocate"); err != nil {
		return fmt.Errorf("save registry: %w", err)
	}

	// 2. Discover + Up + ReadyCheck every DB plugin. The defers run in
	// reverse so the LIFO order matches startup order — handy when the
	// run aborts mid-loop and we want to tear down the providers we
	// did manage to start.
	var dbs []dbInstance
	defer func() {
		for _, db := range dbs {
			db.kill()
		}
	}()
	provider := dbProviderRepo(cfg)
	dbProviderWorktree := worktreeRoot
	if provider != nil {
		dbProviderWorktree = filepath.Join(worktreeRoot, provider.Name)
	}
	// Auto-detect once per `bough create` — at most one nix/docker
	// probe per invocation regardless of how many databases are
	// declared. The result is reused across every dbCfg whose Backend
	// is empty; explicit YAML values bypass it entirely. We fail the
	// whole create up-front rather than letting the plugin Up explode
	// with a confusing nix/docker error mid-way through.
	var detected string
	for _, dbCfg := range cfg.Databases {
		if dbCfg.Backend != "" {
			continue
		}
		d, err := backend.Detect(ctx)
		if err != nil {
			return err
		}
		detected = d
		logf(stderr, "[bough] backend: auto-detected %s", detected)
		break
	}
	for _, dbCfg := range cfg.Databases {
		port := dbPorts[dbCfg.Kind]
		prov, kill, err := pluginhost.Discover(dbCfg.Kind)
		if err != nil {
			return fmt.Errorf("discover %s plugin: %w", dbCfg.Kind, err)
		}
		dbs = append(dbs, dbInstance{cfg: dbCfg, port: port, kill: kill})
		logf(stderr, "[bough] %s: plugin discovered, starting on port %d", dbCfg.Kind, port)
		dataDir := filepath.Join(worktreeRoot, fmt.Sprintf(".local/%s-data", dbCfg.Kind))
		// Inject the backend selector into the plugin's Extras map so
		// the plugin can dispatch between nix / docker / future
		// backends without a proto change. dbCfg.Extras may be nil, so
		// allocate a fresh map either way and copy the user-supplied
		// keys in verbatim. An empty Backend falls back to the
		// auto-detection result resolved once above; an explicit
		// "nix"/"docker" value short-circuits detection.
		extras := make(map[string]string, len(dbCfg.Extras)+2)
		for k, v := range dbCfg.Extras {
			extras[k] = v
		}
		switch {
		case dbCfg.Backend != "":
			extras["backend"] = dbCfg.Backend
		case detected != "":
			extras["backend"] = detected
		}
		if dbCfg.Version != "" {
			extras["version"] = dbCfg.Version
		}
		if err := prov.Up(ctx, api.UpReq{
			Port: port, Datadir: dataDir, WorktreeRoot: dbProviderWorktree,
			SocketDir: dbCfg.SocketDir, InitialDatabases: dbCfg.InitialDatabases, Extras: extras,
		}); err != nil {
			return fmt.Errorf("%s Up: %w", dbCfg.Kind, err)
		}
		ready, err := prov.ReadyCheck(ctx, port, dbCfg.ReadyTimeoutSec)
		if err != nil || !ready {
			return fmt.Errorf("%s ReadyCheck: %w", dbCfg.Kind, err)
		}
		vars, err := prov.EnvVars(ctx, api.EnvVarsReq{
			Port: port, InitialDatabases: dbCfg.InitialDatabases, SocketDir: dbCfg.SocketDir,
		})
		if err != nil {
			return fmt.Errorf("%s EnvVars: %w", dbCfg.Kind, err)
		}
		dbs[len(dbs)-1].envVars = vars
		logf(stderr, "[bough] %s: ready on port %d", dbCfg.Kind, port)
	}

	// 3. git worktree add + direnv + symlinks per repository. We
	// continue on per-repo failure because partial worktree
	// materialisation is more useful than aborting on the first error
	// — the operator can `bough remove` and retry.
	runner := gitwt.NewRunner()
	for _, repo := range cfg.Repositories {
		repoSrc := filepath.Join(monorepoRoot, repo.Name)
		repoDst := filepath.Join(worktreeRoot, repo.Name)
		base, _ := runner.DetectBase(ctx, repoSrc, repo.BranchStrategy)
		created, err := runner.AddOrAttach(ctx, repoSrc, repoDst, name, base)
		if err != nil {
			logf(stderr, "[bough] %s: worktree add FAILED: %v", repo.Name, err)
			continue
		}
		if created {
			logf(stderr, "[bough] %s: created (%s from %s)", repo.Name, name, base)
		} else {
			logf(stderr, "[bough] %s: attached (%s)", repo.Name, name)
		}
		if repo.Direnv {
			envrc := filepath.Join(repoDst, ".envrc")
			if _, statErr := os.Stat(envrc); statErr == nil {
				_ = exec.CommandContext(ctx, "direnv", "allow", envrc).Run()
			}
		}
		for _, sl := range repo.Symlinks {
			target := sl.Target
			link := filepath.Join(repoDst, sl.Link)
			_ = os.Symlink(target, link) // best-effort, OK if already present
		}
	}

	// 4. Render + write .env.local per repository. The Mysql DBCtx is
	// keyed off `kind: mysql` for now; richer engines will expose more
	// distinct contexts in a future schema revision.
	for _, repo := range cfg.Repositories {
		if len(repo.EnvLocal) == 0 {
			continue
		}
		repoDst := filepath.Join(worktreeRoot, repo.Name)
		envCtx := envwriter.Context{
			Worktree: envwriter.WorktreeCtx{Name: name, Root: worktreeRoot},
			Repo:     envwriter.RepoCtx{Name: repo.Name, Path: repoDst},
			Mysql:    dbContextFor("mysql", dbs),
			Ports:    portsCtx,
		}
		rendered, err := envwriter.Render(repo.EnvLocal, envCtx)
		if err != nil {
			return fmt.Errorf("%s env_local render: %w", repo.Name, err)
		}
		// Merge the plugin-supplied env vars on top so monorepo authors
		// can omit BOUGH_MYSQL_* from the YAML when the defaults suffice.
		for _, db := range dbs {
			for k, v := range db.envVars {
				if _, ok := rendered[k]; !ok {
					rendered[k] = v
				}
			}
		}
		dst := filepath.Join(repoDst, ".env.local")
		if err := envwriter.Write(dst, rendered); err != nil {
			return fmt.Errorf("%s .env.local write: %w", repo.Name, err)
		}
		logf(stderr, "[bough] %s: .env.local written (%d keys)", repo.Name, len(rendered))
	}

	// 5. post_create hooks. Best-effort: a failing migration here is
	// reported to stderr but does not unwind the entire create — the
	// operator usually wants the worktree materialised even when seed
	// data is missing.
	for _, repo := range cfg.Repositories {
		if len(repo.PostCreate) == 0 {
			continue
		}
		repoDst := filepath.Join(worktreeRoot, repo.Name)
		for _, line := range repo.PostCreate {
			logf(stderr, "[bough] %s post_create: %s", repo.Name, line)
			c := exec.CommandContext(ctx, "bash", "-c", line)
			c.Dir = repoDst
			c.Stdout = stderr
			c.Stderr = stderr
			if err := c.Run(); err != nil {
				logf(stderr, "[bough] %s post_create FAILED: %v", repo.Name, err)
			}
		}
	}

	// 6. stdout — the WorktreeCreate hook contract REQUIRES exactly the
	// absolute worktree root path on stdout so Claude Code can cd into
	// it. Everything else goes to stderr.
	fmt.Fprintln(stdout, worktreeRoot)
	return nil
}

// allocateRegistry walks every DB and writes the chosen port into reg.
// Returns kind→port for the create loop downstream.
func allocateRegistry(reg registry.Registry, cfg *config.Config, name string) (map[string]int, error) {
	out := make(map[string]int, len(cfg.Databases))
	for _, db := range cfg.Databases {
		// Deterministic re-allocation: if the registry already holds a
		// port for (name, kind), reuse it rather than re-seeding from
		// scratch. The allocator gets the same answer regardless, but
		// short-circuiting here keeps backups noise-free.
		if existing, ok := registry.Get(reg, name, db.Kind); ok {
			out[db.Kind] = existing
			continue
		}
		port, err := allocator.Allocate(name, db.PortRange[0], rangeLen(db.PortRange),
			registry.TakenByKind(reg, db.Kind))
		if err != nil {
			return nil, fmt.Errorf("allocate %s port: %w", db.Kind, err)
		}
		registry.Set(reg, name, db.Kind, port)
		out[db.Kind] = port
	}
	return out, nil
}

func allocateNonDBPorts(reg registry.Registry, cfg *config.Config, name string) (map[string]int, error) {
	out := make(map[string]int, len(cfg.Ports))
	for kind, pr := range cfg.Ports {
		if existing, ok := registry.Get(reg, name, kind); ok {
			out[kind] = existing
			continue
		}
		port, err := allocator.Allocate(name, pr.Range[0], rangeLen(pr.Range),
			registry.TakenByKind(reg, kind))
		if err != nil {
			return nil, fmt.Errorf("allocate %s port: %w", kind, err)
		}
		registry.Set(reg, name, kind, port)
		out[kind] = port
	}
	return out, nil
}

func dbContextFor(kind string, dbs []dbInstance) envwriter.DBCtx {
	for _, d := range dbs {
		if d.cfg.Kind != kind {
			continue
		}
		dir := d.cfg.SocketDir
		if dir == "" {
			dir = "/tmp"
		}
		return envwriter.DBCtx{
			Port:   d.port,
			Host:   "127.0.0.1",
			Socket: filepath.Join(dir, fmt.Sprintf("bough-%s-%d.sock", kind, d.port)),
		}
	}
	return envwriter.DBCtx{Host: "127.0.0.1"}
}

func logf(w interface{ Write([]byte) (int, error) }, format string, a ...any) {
	fmt.Fprintf(w, format+"\n", a...)
}
