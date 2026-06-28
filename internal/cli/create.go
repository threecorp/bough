package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ikeikeikeike/bough/internal/allocator"
	"github.com/ikeikeikeike/bough/internal/backend"
	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/envwriter"
	"github.com/ikeikeikeike/bough/internal/gitwt"
	"github.com/ikeikeikeike/bough/internal/pluginhost"
	"github.com/ikeikeikeike/bough/internal/registry"
	engineapi "github.com/ikeikeikeike/bough/plugins/engine/api"

	"github.com/spf13/cobra"
)

func newCreateCmd() *cobra.Command {
	var (
		name      string
		cwd       string
		stdinJSON bool
		noFetch   bool
		strict    bool
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
			return runCreate(cmd.Context(), cmd.ErrOrStderr(), cmd.OutOrStdout(), cfg, monorepoRoot, name, noFetch, strict)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "worktree name (mutually exclusive with --stdin-json)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "monorepo root (defaults to current working dir; overridden by --stdin-json)")
	cmd.Flags().BoolVar(&stdinJSON, "stdin-json", false, "read {name, cwd} from stdin in Claude Code WorktreeCreate hook format")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "skip `git fetch origin <base>` before worktree add (= use local refs as-is; useful offline)")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero if any repo worktree-add or post_create hook fails (default: best-effort — the worktree path is still emitted and exit stays 0 once the worktree exists)")
	return cmd
}

// engineInstance holds the per-engine side-effects we need to
// remember between the spawn-pass and the env-render pass. The
// connection cleanup closure runs at the end of runCreate regardless
// of partial-success — keeping the gRPC subprocess alive would leak
// after the host returns.
//
// v0.4.0: single-port engines (mysql/postgres/redis/elasticsearch)
// allocate one port under role="main". Multi-port engines extend
// this to a per-role port map in Λ-7.4.
type engineInstance struct {
	cfg     config.Engine
	port    int
	envVars map[string]string
	kill    func()
}

// runCreate is the ordered choreography of allocator → registry →
// gitwt → pluginhost → envwriter → post_create. Errors are
// progressive: per-repo failures log to stderr and continue (the host
// typically converges across two-or-three `bough create` retries),
// but registry / plugin failures abort because they leave the
// operator in an inconsistent state.
//
// Each numbered phase below is a self-contained helper so this body
// reads as the contract: load → allocate → start engines → materialise
// repos → render env → run hooks → emit the worktree path.
func runCreate(ctx context.Context, stderr, stdout io.Writer, cfg *config.Config, monorepoRoot, name string, noFetch, strict bool) error {
	logf(stderr, "[bough] create %s @ %s", name, monorepoRoot)
	worktreeRoot := filepath.Join(monorepoRoot, ".worktrees", name)
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir worktree root: %w", err)
	}

	// 1. Registry: load, allocate, save in one mutation block. The
	// registry is the single source of truth for "which ports does
	// this worktree own"; the allocator only ever sees the map
	// snapshot.
	enginePorts, portsCtx, err := allocateAllPorts(cfg, monorepoRoot, name)
	if err != nil {
		return err
	}

	// 2. Engine plugins: discover binaries, Up + ReadyCheck each, and
	// capture their EnvVars for the env-render pass. The defer kills
	// every started subprocess on the way out — even partial-start
	// engines from a mid-loop error are caught because startEngines
	// returns whatever it managed to bring up.
	engines, err := startEngines(ctx, stderr, cfg, worktreeRoot, enginePorts)
	defer func() {
		for _, e := range engines {
			e.kill()
		}
	}()
	if err != nil {
		return err
	}

	// 3. git worktree add + direnv + symlinks per repository. We
	// continue on per-repo failure because partial worktree
	// materialisation is more useful than aborting on the first
	// error — the operator can `bough remove` and retry.
	failedRepos := materializeRepositories(ctx, stderr, cfg, monorepoRoot, worktreeRoot, name, noFetch)

	// 4. Render + write .env.local per repository.
	if err := renderEnvLocals(stderr, cfg, worktreeRoot, name, engines, portsCtx); err != nil {
		return err
	}

	// 5. post_create hooks. Best-effort: a failing migration here is
	// reported to stderr but does not unwind the entire create — the
	// operator usually wants the worktree materialised even when seed
	// data is missing.
	failedHooks := runPostCreateHooks(ctx, stderr, cfg, worktreeRoot)

	// 6. stdout — the WorktreeCreate hook contract REQUIRES exactly
	// the absolute worktree root path on stdout so Claude Code can
	// cd into it. Everything else goes to stderr. Emit it even on
	// partial failure so the operator still lands in the worktree.
	fmt.Fprintln(stdout, worktreeRoot)

	// 7. Surface partial failures loudly. By default create still
	// returns success once the worktree exists (the hook's cd
	// contract); --strict turns any worktree-add or post_create
	// failure into a non-zero exit for CI / scripted callers.
	if n := len(failedRepos) + len(failedHooks); n > 0 {
		logf(stderr, "[bough] WARNING: create finished with %d problem(s); the worktree exists but its environment may be incomplete:", n)
		for _, r := range failedRepos {
			logf(stderr, "[bough]   - worktree add failed: %s", r)
		}
		for _, h := range failedHooks {
			logf(stderr, "[bough]   - post_create failed: %s", h)
		}
		if strict {
			return fmt.Errorf("create %s: %d post-setup problem(s) (worktree add: %d, post_create: %d) with --strict",
				name, n, len(failedRepos), len(failedHooks))
		}
	}
	return nil
}

// allocateAllPorts loads the registry, allocates one port per engine
// role and one per non-engine kind, and saves the registry under the
// `allocate` reason. The save happens before any plugin is contacted
// so a plugin-side failure cannot leave the registry inconsistent.
// v0.4 path: prefer .bough-ports.json, fall back to the YAML-declared
// path (typically v0.3's .worktree-ports.json) when the canonical
// file is absent; the registry loader auto-upgrades legacy keys.
//
// Returns (enginePorts kind→port, nonEnginePorts kind→port).
func allocateAllPorts(cfg *config.Config, monorepoRoot, name string) (map[string]int, map[string]int, error) {
	registryPath := resolveRegistryPath(monorepoRoot, cfg.Registry.Path)
	store := registry.NewStore(registryPath, cfg.Registry.BackupDir)
	reg, err := store.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("load registry: %w", err)
	}
	enginePorts, err := allocateEngines(reg, cfg, name)
	if err != nil {
		return nil, nil, err
	}
	nonEnginePorts, err := allocateNonEnginePorts(reg, cfg, name)
	if err != nil {
		return nil, nil, err
	}
	if err := store.Save(reg, "allocate"); err != nil {
		return nil, nil, fmt.Errorf("save registry: %w", err)
	}
	return enginePorts, nonEnginePorts, nil
}

// startEngines walks every engine declared in cfg, discovers the
// matching `bough-plugin-<kind>` binary on PATH, calls Up +
// ReadyCheck against the allocated port, and stashes the returned
// EnvVars for the env-render pass.
//
// Returns whatever engines it managed to bring up — even on error —
// so the caller's defer can kill every started subprocess without
// further bookkeeping.
//
// The backend auto-detect runs at most once per `bough create`: the
// result is reused across every engine whose YAML left Backend
// empty; explicit YAML values bypass it entirely.
func startEngines(
	ctx context.Context,
	stderr io.Writer,
	cfg *config.Config,
	worktreeRoot string,
	enginePorts map[string]int,
) ([]engineInstance, error) {
	engineProviderWorktree := worktreeRoot
	if provider := engineProviderRepo(cfg); provider != nil {
		engineProviderWorktree = filepath.Join(worktreeRoot, provider.Name)
	}

	detected, err := detectBackendIfNeeded(ctx, stderr, cfg)
	if err != nil {
		return nil, err
	}

	engines := make([]engineInstance, 0, len(cfg.Engines))
	for _, eng := range cfg.Engines {
		port := enginePorts[eng.Kind]
		prov, kill, err := pluginhost.Discover(eng.Kind)
		if err != nil {
			return engines, fmt.Errorf("discover %s plugin: %w", eng.Kind, err)
		}
		engines = append(engines, engineInstance{cfg: eng, port: port, kill: kill})
		logf(stderr, "[bough] %s: plugin discovered, starting on port %d", eng.Kind, port)

		extras := buildEngineExtras(eng, detected)
		ports := []engineapi.PortSpec{{Role: "main", Port: port}}
		resources := toResourceSpecs(eng.InitialResources)
		dataDir := filepath.Join(worktreeRoot, fmt.Sprintf(".local/%s-data", eng.Kind))

		if err := prov.Up(ctx, &engineapi.UpReq{
			Ports:            ports,
			Datadir:          dataDir,
			WorktreeRoot:     engineProviderWorktree,
			SocketDir:        eng.SocketDir,
			InitialResources: resources,
			Extras:           extras,
		}); err != nil {
			return engines, fmt.Errorf("%s Up: %w", eng.Kind, err)
		}
		ready, err := prov.ReadyCheck(ctx, []int{port}, eng.ReadyTimeoutSec)
		if err != nil || !ready {
			return engines, fmt.Errorf("%s ReadyCheck: %w", eng.Kind, err)
		}
		vars, err := prov.EnvVars(ctx, &engineapi.EnvVarsReq{
			Ports:            ports,
			InitialResources: resources,
			SocketDir:        eng.SocketDir,
		})
		if err != nil {
			return engines, fmt.Errorf("%s EnvVars: %w", eng.Kind, err)
		}
		engines[len(engines)-1].envVars = vars
		logf(stderr, "[bough] %s: ready on port %d", eng.Kind, port)
	}
	return engines, nil
}

// detectBackendIfNeeded runs backend.Detect once per create call, but
// only when at least one engine left Backend empty in the YAML.
// Returns the detected backend ("" if not needed); fails hard on a
// genuine detection error since every YAML-empty engine downstream
// would inherit the empty string and pick the wrong path.
func detectBackendIfNeeded(ctx context.Context, stderr io.Writer, cfg *config.Config) (string, error) {
	for _, eng := range cfg.Engines {
		if eng.Backend == "" {
			d, err := backend.Detect(ctx)
			if err != nil {
				return "", err
			}
			logf(stderr, "[bough] backend: auto-detected %s", d)
			return d, nil
		}
	}
	return "", nil
}

// buildEngineExtras assembles the extras map the plugin Up call sees:
// every engine-declared extra verbatim, plus `backend` (explicit YAML
// value beats the auto-detect result, both beat the plugin default)
// and `version` when set.
func buildEngineExtras(eng config.Engine, detected string) map[string]string {
	extras := make(map[string]string, len(eng.Extras)+2)
	for k, v := range eng.Extras {
		extras[k] = v
	}
	switch {
	case eng.Backend != "":
		extras["backend"] = eng.Backend
	case detected != "":
		extras["backend"] = detected
	}
	if eng.Version != "" {
		extras["version"] = eng.Version
	}
	return extras
}

// materializeRepositories runs `git worktree add` + direnv + symlink
// drops per declared repository. Per-repo failures are logged and
// the loop continues — partial materialisation is more useful to the
// operator than aborting on the first error.
func materializeRepositories(
	ctx context.Context,
	stderr io.Writer,
	cfg *config.Config,
	monorepoRoot, worktreeRoot, name string,
	noFetch bool,
) []string {
	var failed []string
	runner := gitwt.NewRunner()
	runner.Fetch = !noFetch
	for _, repo := range cfg.Repositories {
		repoSrc := filepath.Join(monorepoRoot, repo.Name)
		repoDst := filepath.Join(worktreeRoot, repo.Name)
		// The declared branch_strategy is authoritative — it is a required
		// field stating which branch the operator wants worktrees based on.
		// origin/HEAD auto-detection is only a fallback for when it is
		// somehow empty. (Previously branch_strategy was passed as
		// DetectBase's OWN fallback, and DetectBase reads origin/HEAD first,
		// so a `git clone --local` whose origin/HEAD mirrored the source's
		// checked-out feature branch silently overrode an explicit
		// `branch_strategy: develop`.)
		detected, _ := runner.DetectBase(ctx, repoSrc, "")
		base := chooseBase(repo.BranchStrategy, detected)
		created, err := runner.AddOrAttach(ctx, repoSrc, repoDst, name, base)
		if err != nil {
			logf(stderr, "[bough] %s: worktree add FAILED: %v", repo.Name, err)
			failed = append(failed, repo.Name)
			continue
		}
		sha, _ := runner.HeadSHA(ctx, repoDst)
		baseLabel := base
		if runner.Fetch {
			baseLabel = "origin/" + base
		}
		if created {
			logf(stderr, "[bough] %s: created (%s from %s @ %s)", repo.Name, name, baseLabel, sha)
		} else {
			logf(stderr, "[bough] %s: attached (%s @ %s)", repo.Name, name, sha)
		}
		if repo.Direnv {
			envrc := filepath.Join(repoDst, ".envrc")
			if _, statErr := os.Stat(envrc); statErr == nil {
				_ = exec.CommandContext(ctx, "direnv", "allow", envrc).Run()
			}
		}
		for _, sl := range repo.Symlinks {
			_ = os.Symlink(sl.Target, filepath.Join(repoDst, sl.Link)) // best-effort
		}
	}
	return failed
}

// chooseBase decides which branch a new worktree is cut from. The
// explicitly-declared branch_strategy wins; the origin/HEAD-detected
// branch is used only when branch_strategy is empty. This keeps an
// operator's `.bough.yaml` authoritative over a clone's recorded
// origin/HEAD — which, for a `git clone --local`, mirrors whatever the
// source happened to have checked out and is not necessarily the
// intended base.
func chooseBase(branchStrategy, detected string) string {
	if strings.TrimSpace(branchStrategy) != "" {
		return branchStrategy
	}
	return detected
}

// renderEnvLocals walks repositories that declare env_local templates
// and writes the rendered .env.local. Engine-emitted vars (EnvVars
// from each plugin) get merged in last so the host can render keys
// the operator did not have to enumerate by hand.
func renderEnvLocals(
	stderr io.Writer,
	cfg *config.Config,
	worktreeRoot, name string,
	engines []engineInstance,
	portsCtx map[string]int,
) error {
	for _, repo := range cfg.Repositories {
		if len(repo.EnvLocal) == 0 {
			continue
		}
		repoDst := filepath.Join(worktreeRoot, repo.Name)
		envCtx := envwriter.Context{
			Worktree: envwriter.WorktreeCtx{Name: name, Root: worktreeRoot},
			Repo:     envwriter.RepoCtx{Name: repo.Name, Path: repoDst},
			Mysql:    engineContextFor("mysql", engines),
			Ports:    portsCtx,
		}
		rendered, err := envwriter.Render(repo.EnvLocal, envCtx)
		if err != nil {
			return fmt.Errorf("%s env_local render: %w", repo.Name, err)
		}
		for _, e := range engines {
			for k, v := range e.envVars {
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
	return nil
}

// runPostCreateHooks fires each repository's `post_create:` lines in
// declaration order. Failures log to stderr and the loop continues —
// a failed migration should not unwind the entire create, since the
// worktree materialisation itself is still valuable to the operator.
func runPostCreateHooks(ctx context.Context, stderr io.Writer, cfg *config.Config, worktreeRoot string) []string {
	var failed []string
	for _, repo := range cfg.Repositories {
		if len(repo.PostCreate) == 0 {
			continue
		}
		repoDst := filepath.Join(worktreeRoot, repo.Name)
		fileEnv := parseEnvLocal(filepath.Join(repoDst, ".env.local"))
		for _, line := range repo.PostCreate {
			logf(stderr, "[bough] %s post_create: %s", repo.Name, line)
			c := exec.CommandContext(ctx, "bash", "-c", line)
			c.Dir = repoDst
			c.Env = append(os.Environ(), fileEnv...)
			c.Stdout = stderr
			c.Stderr = stderr
			if err := c.Run(); err != nil {
				logf(stderr, "[bough] %s post_create FAILED: %v", repo.Name, err)
				failed = append(failed, fmt.Sprintf("%s: %s", repo.Name, line))
			}
		}
	}
	return failed
}

// allocateEngines walks every engine and writes the chosen port
// (role="main") into reg. Returns kind→port for the create loop
// downstream. v0.4.0: single-port engines only — multi-port engines
// (rabbitmq AMQP+Management, kafka broker+controller) extend this in
// Λ-7.4 to allocate per-role.
func allocateEngines(reg registry.Registry, cfg *config.Config, name string) (map[string]int, error) {
	out := make(map[string]int, len(cfg.Engines))
	for _, eng := range cfg.Engines {
		mainRange, ok := eng.PortRanges["main"]
		if !ok {
			// Λ-7.4 will iterate every role; for now we require
			// "main" so single-port plugins boot. A multi-port
			// engine that declares no "main" is a config error.
			return nil, fmt.Errorf("config: engines[%s].port_ranges must declare 'main' (multi-port allocation lands in Λ-7.4)", eng.Kind)
		}
		key := eng.Kind + ".main"
		if existing, ok := registry.Get(reg, name, key); ok {
			out[eng.Kind] = existing
			continue
		}
		port, err := allocator.Allocate(name, "main", mainRange[0], rangeLen(mainRange),
			registry.TakenByKind(reg, key))
		if err != nil {
			return nil, fmt.Errorf("allocate %s.main port: %w", eng.Kind, err)
		}
		registry.Set(reg, name, key, port)
		out[eng.Kind] = port
	}
	return out, nil
}

func allocateNonEnginePorts(reg registry.Registry, cfg *config.Config, name string) (map[string]int, error) {
	out := make(map[string]int, len(cfg.Ports))
	for kind, pr := range cfg.Ports {
		key := kind + ".main"
		if existing, ok := registry.Get(reg, name, key); ok {
			out[kind] = existing
			continue
		}
		port, err := allocator.Allocate(name, "main", pr.Range[0], rangeLen(pr.Range),
			registry.TakenByKind(reg, key))
		if err != nil {
			return nil, fmt.Errorf("allocate %s port: %w", kind, err)
		}
		registry.Set(reg, name, key, port)
		out[kind] = port
	}
	return out, nil
}

func engineContextFor(kind string, engines []engineInstance) envwriter.DBCtx {
	for _, e := range engines {
		if e.cfg.Kind != kind {
			continue
		}
		dir := e.cfg.SocketDir
		if dir == "" {
			dir = "/tmp"
		}
		return envwriter.DBCtx{
			Port:   e.port,
			Host:   "127.0.0.1",
			Socket: filepath.Join(dir, fmt.Sprintf("bough-%s-%d.sock", kind, e.port)),
		}
	}
	return envwriter.DBCtx{Host: "127.0.0.1"}
}

func toResourceSpecs(in []config.InitialResource) []engineapi.ResourceSpec {
	out := make([]engineapi.ResourceSpec, len(in))
	for i, r := range in {
		out[i] = engineapi.ResourceSpec{Type: r.Type, Name: r.Name, Params: r.Params}
	}
	return out
}

// resolveRegistryPath picks the v0.4 canonical `.bough-ports.json`
// when it exists, otherwise falls back to whatever the YAML declared
// (typically v0.3's `.worktree-ports.json`). The host's registry
// loader auto-upgrades legacy keys in either case, so the operator
// can rename at any pace during the v0.4.x transition.
func resolveRegistryPath(monorepoRoot, yamlPath string) string {
	canonical := filepath.Join(monorepoRoot, registry.CanonicalPath)
	if _, err := os.Stat(canonical); err == nil {
		return canonical
	}
	// `yamlPath` is whatever the operator wrote in registry.path —
	// honour it relative to the monorepo root.
	return filepath.Join(monorepoRoot, yamlPath)
}

// parseEnvLocal reads a `.env.local` file and returns its `KEY=VALUE`
// lines verbatim so the caller can pass them through to a child
// process's exec.Cmd.Env. Comments (`#`-prefixed) and blank lines
// are skipped; lines without an `=` are silently dropped.
//
// IMPORTANT: this parser does NOT do shell unquoting / interpolation
// of values. The bough envwriter emits raw values without surrounding
// quotes (matching the historical direnv `dotenv_if_exists .env.local`
// idiom) and bash `source` would choke on the `(`, `&`, `?` chars
// many DSNs / URLs contain — that exact failure mode is the bug this
// helper exists to bypass.
func parseEnvLocal(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var env []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		env = append(env, line)
	}
	return env
}

func logf(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, format+"\n", a...)
}
