package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ikeikeikeike/bough/internal/allocator"
	"github.com/ikeikeikeike/bough/internal/backend"
	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/envwriter"
	"github.com/ikeikeikeike/bough/internal/gitwt"
	"github.com/ikeikeikeike/bough/internal/pluginhost"
	"github.com/ikeikeikeike/bough/internal/registry"
	"github.com/ikeikeikeike/bough/internal/termio"
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
// reads as the contract: load → allocate → materialise repos → start
// engines → render env → run hooks → emit the worktree path.
func runCreate(ctx context.Context, stderr, stdout io.Writer, cfg *config.Config, monorepoRoot, name string, noFetch, strict bool) error {
	// One mutex per fd: route every create-path stderr write (logf, the
	// spinner) through the shared termio wrapper so pluginhost's hclog
	// lines — which target termio.Stderr from their own goroutines —
	// cannot interleave with the spinner redraw (issue #67). For the
	// real os.Stderr this IS termio.Stderr; test buffers get a private
	// wrapper and behave as before.
	stderr = termio.Wrap(stderr)
	logf(stderr, "[bough] create %s @ %s", name, monorepoRoot)
	warnIfRootNotGit(stderr, cfg, monorepoRoot)
	worktreeRoot := filepath.Join(worktreesDir(monorepoRoot), name)
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

	// 2. git worktree add + direnv + symlinks per repository. We
	// continue on per-repo failure because partial worktree
	// materialisation is more useful than aborting on the first
	// error — the operator can `bough remove` and retry. This must run
	// BEFORE starting engines: an engine-provider repo's worktree
	// destination and its engine's deployed flake dir are the same
	// path (<worktreeRoot>/<repo.Name>), and `git worktree add` fails
	// outright against a non-empty destination — populating it via
	// deployFlake first would break worktree materialisation for that
	// repo on every single `bough create`.
	failedRepos := materializeRepositories(ctx, stderr, cfg, monorepoRoot, worktreeRoot, name, noFetch)

	// 3. Engine plugins: discover binaries, Up + ReadyCheck each, and
	// capture their EnvVars for the env-render pass. The defer kills
	// every started subprocess on the way out — even partial-start
	// engines from a mid-loop error are caught because startEngines
	// returns whatever it managed to bring up.
	engines, err := startEngines(ctx, stderr, cfg, worktreeRoot, enginePorts, pluginhost.Discover)
	defer func() {
		for _, e := range engines {
			e.kill()
		}
	}()
	if err != nil {
		// The plugin subprocess's own logs are suppressed at the default
		// Warn level, so an engine-start failure is otherwise opaque —
		// signpost the escape hatch unless the operator already enabled
		// it. Skip the hint when backend detection itself failed
		// (backend.ErrNoBackend): that happens before any plugin
		// subprocess is spawned, so there is no plugin log to re-run
		// for, and the error already names the real remediation
		// (engines[].backend in .bough.yaml).
		if os.Getenv(pluginhost.EnvPluginLog) == "" && !errors.Is(err, backend.ErrNoBackend) {
			err = fmt.Errorf("%w (re-run with %s=debug for the plugin's own logs)", err, pluginhost.EnvPluginLog)
		}
		return err
	}

	// Repos that failed to materialise (clone / worktree-add) are skipped
	// in the env_local + post_create passes below: their worktree dir does
	// not exist, so rendering a `.env.local` there would only MkdirAll a
	// bogus directory and run post_create hooks against empty content.
	skipRepo := make(map[string]bool, len(failedRepos))
	for _, r := range failedRepos {
		skipRepo[r] = true
	}

	// 4. Render + write .env.local per repository. Best-effort like
	// post_create: a bad template / unwritable repo must not abort before the
	// worktree-path stdout emit + failure summary below.
	failedEnv := renderEnvLocals(stderr, cfg, worktreeRoot, name, engines, portsCtx, skipRepo)

	// A repo whose .env.local failed to render/write this run must not
	// have post_create run against it: there is nothing (or a stale
	// prior run's file) to inject, and post_create's own env would
	// silently interpolate to empty strings with no link back to this
	// earlier failure. Same treatment as a failed worktree-add.
	for _, e := range failedEnv {
		skipRepo[e.Repo] = true
	}

	// 5. post_create hooks. Best-effort: a failing migration here is
	// reported to stderr but does not unwind the entire create — the
	// operator usually wants the worktree materialised even when seed
	// data is missing.
	failedHooks := runPostCreateHooks(ctx, stderr, cfg, worktreeRoot, skipRepo)

	// 5b. Expose the monorepo's project-scoped evolved skills to the worktree
	// session. `claude --worktree` cd's into <worktreeRoot> — a non-git
	// container whose git walk-up cannot reach the monorepo root's
	// .claude/skills — so without an explicit symlink the worktree session
	// would load no project skills. Best-effort.
	linkWorktreeSkills(stderr, monorepoRoot, worktreeRoot)

	// 6. stdout — the WorktreeCreate hook contract REQUIRES exactly
	// the absolute worktree root path on stdout so Claude Code can
	// cd into it. Everything else goes to stderr. Emit it even on
	// partial failure so the operator still lands in the worktree.
	fmt.Fprintln(stdout, worktreeRoot)

	// 7. Surface partial failures loudly. By default create still
	// returns success once the worktree exists (the hook's cd
	// contract); --strict turns any worktree-add or post_create
	// failure into a non-zero exit for CI / scripted callers.
	if n := len(failedRepos) + len(failedEnv) + len(failedHooks); n > 0 {
		logf(stderr, "[bough] WARNING: create finished with %d problem(s); the worktree exists but its environment may be incomplete:", n)
		for _, r := range failedRepos {
			logf(stderr, "[bough]   - worktree add failed: %s", r)
		}
		for _, e := range failedEnv {
			logf(stderr, "[bough]   - env_local failed: %s (%s)", e.Repo, e.Detail)
		}
		for _, h := range failedHooks {
			logf(stderr, "[bough]   - post_create failed: %s", h)
		}
		if strict {
			return fmt.Errorf("create %s: %d post-setup problem(s) (worktree add: %d, env_local: %d, post_create: %d) with --strict",
				name, n, len(failedRepos), len(failedEnv), len(failedHooks))
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
//
// discover is the plugin lookup (production: pluginhost.Discover),
// injected so tests can substitute a fake EngineProvider and pin the
// per-phase error wrapping without spawning subprocesses (#68).
func startEngines(
	ctx context.Context,
	stderr io.Writer,
	cfg *config.Config,
	worktreeRoot string,
	enginePorts map[string]int,
	discover func(kind string) (engineapi.EngineProvider, func(), error),
) ([]engineInstance, error) {
	engineProviderWorktree := worktreeRoot
	if provider := engineProviderRepo(cfg); provider != nil {
		engineProviderWorktree = filepath.Join(worktreeRoot, provider.Name)
	}

	// A cold/NFS-mounted nix store or an unresponsive docker daemon can
	// each take up to detectTimeout; animate a spinner across the call
	// like every other multi-second step in create so an interactive
	// operator sees liveness instead of a frozen prompt. No-op (and
	// near-instant) when every engine already pins Backend explicitly.
	sp := startSpinner(stderr, "backend: detecting nix/docker")
	detected, err := detectBackendIfNeeded(ctx, stderr, cfg)
	sp.Stop()
	if err != nil {
		return nil, err
	}

	engines := make([]engineInstance, 0, len(cfg.Engines))
	for _, eng := range cfg.Engines {
		port := enginePorts[eng.Kind]
		prov, kill, err := discover(eng.Kind)
		if err != nil {
			return engines, fmt.Errorf("discover %s plugin: %w", eng.Kind, err)
		}
		engines = append(engines, engineInstance{cfg: eng, port: port, kill: kill})
		logf(stderr, "[bough] %s: plugin discovered, starting on port %d", eng.Kind, port)

		extras := buildEngineExtras(eng, detected)
		ports := []engineapi.PortSpec{{Role: "main", Port: port}}
		resources := toResourceSpecs(eng.InitialResources)
		dataDir := filepath.Join(worktreeRoot, fmt.Sprintf(".local/%s-data", eng.Kind))

		// Up + ReadyCheck can block for seconds (image pull, the mysql
		// temp→final mysqld restart, ES bootstrap). Animate a spinner for
		// the whole wait so an interactive operator sees liveness instead
		// of a frozen prompt; the closure lets one deferred Stop() cover
		// every exit path. On the hook (non-TTY) the spinner is inert.
		sp := startSpinner(stderr, fmt.Sprintf("%s: starting on port %d", eng.Kind, port))
		vars, err := func() (map[string]string, error) {
			defer sp.Stop()
			if err := prov.Up(ctx, &engineapi.UpReq{
				Ports:            ports,
				Datadir:          dataDir,
				WorktreeRoot:     engineProviderWorktree,
				SocketDir:        eng.SocketDir,
				InitialResources: resources,
				Extras:           extras,
				Plugins:          toPluginSpecs(eng.Plugins),
			}); err != nil {
				return nil, fmt.Errorf("%s Up: %w", eng.Kind, err)
			}
			ready, err := prov.ReadyCheck(ctx, []int{port}, eng.ReadyTimeoutSec)
			if err != nil {
				return nil, fmt.Errorf("%s ReadyCheck: %w", eng.Kind, err)
			}
			if !ready {
				// (ready=false, err=nil) is a timeout; format a real message
				// rather than `%w`-ing the nil error into `%!w(<nil>)`.
				return nil, fmt.Errorf("%s ReadyCheck: not ready within %ds", eng.Kind, eng.ReadyTimeoutSec)
			}
			vars, err := prov.EnvVars(ctx, &engineapi.EnvVarsReq{
				Ports:            ports,
				InitialResources: resources,
				SocketDir:        eng.SocketDir,
			})
			if err != nil {
				return nil, fmt.Errorf("%s EnvVars: %w", eng.Kind, err)
			}
			return vars, nil
		}()
		if err != nil {
			return engines, err
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
// and `version` when set. Explicit beats auto-detect whether the
// operator named the backend via the dedicated `eng.Backend` field or
// directly through `extras.backend` — extras are documented as copied
// verbatim, so auto-detect must only fill a genuine gap, never
// silently discard a value already present in the map.
func buildEngineExtras(eng config.Engine, detected string) map[string]string {
	extras := make(map[string]string, len(eng.Extras)+2)
	for k, v := range eng.Extras {
		extras[k] = v
	}
	switch {
	case eng.Backend != "":
		extras["backend"] = eng.Backend
	case extras["backend"] != "":
		// operator already set it via extras: — leave it alone.
	case detected != "":
		extras["backend"] = detected
	}
	if eng.Version != "" {
		extras["version"] = eng.Version
	}
	if eng.Compose != nil {
		extras["compose.file"] = eng.Compose.File
		extras["compose.service"] = eng.Compose.Service
		extras["compose.target_port"] = strconv.Itoa(eng.Compose.TargetPort)
		if eng.Compose.Project != "" {
			extras["compose.project"] = eng.Compose.Project
		}
		if eng.Compose.EnvPrefix != "" {
			extras["compose.env_prefix"] = eng.Compose.EnvPrefix
		}
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
		repoSrc := resolveRepoSrc(monorepoRoot, repo.Name)
		repoDst := filepath.Join(worktreeRoot, repo.Name)
		// Acquire the repo first if it is not already present and a
		// `source:` is declared (git URL → clone, local path → clone
		// --local). Best-effort, like the rest of this loop: a clone
		// failure logs + skips the repo (and --strict turns the run
		// non-zero). No source + not present → also a per-repo failure.
		// Presence is "is a git repo" (a `.git` entry), NOT merely "is a
		// directory": a stray/empty/partial-clone leftover dir must not
		// shadow the source: clone — otherwise every future create runs git
		// ops against a non-repo and fails with an opaque exit-128 forever.
		if !isGitRepo(repoSrc) {
			if repo.Source == "" {
				logf(stderr, "[bough] %s: not present at %s and no `source:` to clone from", repo.Name, repoSrc)
				failed = append(failed, repo.Name)
				continue
			}
			// A first-time clone of a large repo is the longest silent gap
			// in create (the user sees nothing between engines-ready and
			// the worktree appearing); animate a spinner across it.
			// resolveRepoSrc points a fresh clone at <root>/.bough/repos/<name>,
			// whose parent may not exist yet — git clone creates the leaf
			// dir but not intermediate parents.
			if err := os.MkdirAll(filepath.Dir(repoSrc), 0o755); err != nil {
				logf(stderr, "[bough] %s: mkdir %s FAILED: %v", repo.Name, filepath.Dir(repoSrc), err)
				failed = append(failed, repo.Name)
				continue
			}
			sp := startSpinner(stderr, fmt.Sprintf("%s: cloning from %s", repo.Name, repo.Source))
			cerr := runner.Clone(ctx, repo.Source, repoSrc, monorepoRoot)
			sp.Stop()
			if cerr != nil {
				logf(stderr, "[bough] %s: clone from %s FAILED: %v", repo.Name, repo.Source, cerr)
				failed = append(failed, repo.Name)
				continue
			}
			logf(stderr, "[bough] %s: cloned from %s", repo.Name, repo.Source)
		}
		// The declared branch_strategy is authoritative when set; when it
		// is empty bough falls back to origin/HEAD (the repo's default
		// branch). branch_strategy must win over origin/HEAD — otherwise a
		// `git clone --local` whose origin/HEAD mirrored the source's
		// checked-out feature branch would silently override an explicit
		// `branch_strategy: develop` (the v0.9.15 fix; see chooseBase).
		detected, _ := runner.DetectBase(ctx, repoSrc, "")
		base := chooseBase(repo.BranchStrategy, detected)
		if base == "" {
			// No branch_strategy AND origin/HEAD unset (a repo with a
			// remote but no `git remote set-head`). Fail with a clear
			// message instead of `git worktree add -b <branch> ""` →
			// `fatal: invalid reference` (the opaque exit-128 class).
			logf(stderr, "[bough] %s: no branch_strategy and could not detect a default branch (origin/HEAD unset) — set repositories[].branch_strategy", repo.Name)
			failed = append(failed, repo.Name)
			continue
		}
		created, effectiveBase, err := runner.AddOrAttach(ctx, repoSrc, repoDst, name, base)
		if err != nil {
			logf(stderr, "[bough] %s: worktree add FAILED: %v", repo.Name, err)
			failed = append(failed, repo.Name)
			continue
		}
		sha, _ := runner.HeadSHA(ctx, repoDst)
		if created {
			// effectiveBase is what AddOrAttach actually branched from —
			// origin/<base> only when the fetch truly succeeded, else the
			// local <base>. Logging runner.Fetch's intent instead would
			// claim "from origin/<base>" even on an offline fallback.
			logf(stderr, "[bough] %s: created (%s from %s @ %s)", repo.Name, name, effectiveBase, sha)
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
	if s := strings.TrimSpace(branchStrategy); s != "" {
		// Return the TRIMMED value: deciding on TrimSpace but returning the
		// raw string let a quoted `branch_strategy: "  develop  "` reach
		// `git worktree add -b <name> "  develop  "` as an invalid ref.
		return s
	}
	return strings.TrimSpace(detected)
}

// isDir reports whether p exists and is a directory.
func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// isGitRepo reports whether p is (the root of) a git repository — i.e. a
// `.git` entry is present (a dir for a normal clone/checkout, a gitfile
// for a worktree/submodule). This is the "is this repo already acquired?"
// guard before cloning a declared `source:` — deliberately stricter than
// isDir so a stray, empty, or partial-clone leftover directory does not
// masquerade as the repo and permanently shadow the clone.
func isGitRepo(p string) bool {
	_, err := os.Lstat(filepath.Join(p, ".git"))
	return err == nil
}

// insideGitWorkTree reports whether dir sits inside a git work tree —
// the exact condition Claude Code's `--worktree` flag enforces (its
// "Can only use --worktree in a git repository" error is this check
// failing). Walks up like git itself, so a monorepo root nested inside
// a parent repo also counts.
//
// determined is false when git could not even be run (binary missing
// from PATH, dir unreadable): git ran-and-said-no is an *exec.ExitError
// and yields a definite (false, true), but a launch failure must NOT be
// reported as a confident "not a git repo" — callers skip their warning
// rather than mis-diagnose a PATH problem as a missing `git init`.
func insideGitWorkTree(dir string) (inside, determined bool) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	if err == nil {
		return strings.TrimSpace(string(out)) == "true", true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, true
	}
	return false, false
}

// worktreeSourceRepo returns the source repository a linked worktree at
// worktreeDst was created from, read from the worktree's OWN gitlink
// (`git rev-parse --git-common-dir` resolves to `<sourceRepo>/.git`).
// This is authoritative at remove time — unlike re-deriving the source
// via resolveRepoSrc, which reads live on-disk state and can drift to a
// different location than create used (e.g. after the operator migrates
// a checkout into .bough/repos/), pointing `git worktree remove` at a
// repo that never registered this worktree. ok is false when worktreeDst
// is absent or not a linked worktree, so the caller can fall back.
func worktreeSourceRepo(worktreeDst string) (string, bool) {
	out, err := exec.Command("git", "-C", worktreeDst, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", false
	}
	commonDir := strings.TrimSpace(string(out))
	if commonDir == "" {
		return "", false
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktreeDst, commonDir)
	}
	// commonDir is the shared <sourceRepo>/.git; its parent is the repo.
	return filepath.Dir(commonDir), true
}

// warnIfRootNotGit prints a heads-up when the monorepo root is not inside
// a git work tree. bough's WorktreeCreate hook still lets `claude
// --worktree` run there (the hook is the documented non-git escape
// hatch), but Claude Code keeps such hook-based worktree sessions
// anchored to the launch directory — so `claude --worktree <name>
// --resume <id>` (the git-native resume path, which looks in the
// worktree's own project bucket) cannot find them. Plain `claude
// --resume <id>` from the monorepo root does find them. Initialising the
// root as a git repo switches Claude Code onto the git-native path,
// making `--worktree ... --resume` work.
//
// bough only SUGGESTS the .gitignore lines; it never edits .gitignore
// itself — the repo's ignore policy is the operator's to own. The
// suggested lines reflect the layout THIS monorepo actually uses (see
// gitignoreSuggestions), so an operator on the legacy layout is not
// told to ignore paths that do not exist while the real ones leak in.
func warnIfRootNotGit(stderr io.Writer, cfg *config.Config, monorepoRoot string) {
	if inside, determined := insideGitWorkTree(monorepoRoot); inside || !determined {
		return
	}
	logf(stderr, "[bough] note: %s is not a git repository.", monorepoRoot)
	logf(stderr, "[bough]   `claude --worktree <name> --resume <id>` will NOT find sessions started here")
	logf(stderr, "[bough]   (Claude Code anchors hook-based worktree sessions to the launch dir). Resume with")
	logf(stderr, "[bough]   plain `claude --resume <id>` from %s instead.", monorepoRoot)
	logf(stderr, "[bough]   To enable `--worktree ... --resume`, `git init` this dir and .gitignore:")
	for _, entry := range gitignoreSuggestions(cfg, monorepoRoot) {
		logf(stderr, "[bough]     %s", entry)
	}
}

// gitignoreSuggestions returns the .gitignore entries that actually
// cover this monorepo's bough-generated artifacts under whichever
// layout is in use: `.bough/` (registry + v0.11 checkouts), the real
// worktrees dir (`worktrees/` or the legacy hidden `.worktrees/`), and
// — for a monorepo still on the pre-v0.11 layout — every sub-repo whose
// checkout sits directly at the root, which `.bough/` does not cover.
func gitignoreSuggestions(cfg *config.Config, monorepoRoot string) []string {
	set := map[string]struct{}{
		boughDir + "/": {},
		filepath.Base(worktreesDir(monorepoRoot)) + "/": {},
	}
	for _, repo := range cfg.Repositories {
		if resolveRepoSrc(monorepoRoot, repo.Name) == filepath.Join(monorepoRoot, repo.Name) {
			set[repo.Name+"/"] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for e := range set {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// ensureSymlink makes linkPath an absolute symlink to target, idempotently. An
// already-correct symlink is a no-op; a stale symlink is repointed; a real
// (non-symlink) file/dir is refused so a hand-authored path is never clobbered.
func ensureSymlink(target, linkPath string) error {
	// Guarantee an absolute target so the link resolves the same regardless of
	// the reader's CWD (the contract above). Evolved-skill sources can be
	// relative when BOUGH_HOMUNCULUS_DIR is set to a relative path; a raw
	// os.Symlink of a relative target would resolve against linkPath's dir and
	// dangle.
	if abs, err := filepath.Abs(target); err == nil {
		target = abs
	}
	if info, err := os.Lstat(linkPath); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s exists and is not a symlink", linkPath)
		}
		if cur, _ := os.Readlink(linkPath); cur == target {
			return nil // already correct
		}
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("remove stale link: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.Symlink(target, linkPath)
}

// linkWorktreeSkills symlinks <worktreeRoot>/.claude/skills ->
// <monorepoRoot>/.claude/skills so the worktree's Claude session loads the
// project-scoped evolved skills. The session cd's into the worktree (a non-git
// container whose git walk-up can't reach the monorepo root), so the symlink is
// required for project-scoped skills to be visible. Best-effort; a real
// .claude/skills dir already in the worktree is left untouched.
func linkWorktreeSkills(stderr io.Writer, monorepoRoot, worktreeRoot string) {
	src := filepath.Join(monorepoRoot, ".claude", "skills")
	if err := os.MkdirAll(src, 0o755); err != nil {
		logf(stderr, "[bough] .claude/skills: mkdir %s failed: %v", src, err)
		return
	}
	dst := filepath.Join(worktreeRoot, ".claude", "skills")
	if err := ensureSymlink(src, dst); err != nil {
		logf(stderr, "[bough] .claude/skills: %v", err)
		return
	}
	logf(stderr, "[bough] .claude/skills → %s", src)
}

// renderEnvLocals walks repositories that declare env_local templates
// and writes the rendered .env.local. Engine-emitted vars (EnvVars
// from each plugin) get merged in last so the host can render keys
// the operator did not have to enumerate by hand.
// envLocalFailure names a repo whose .env.local render or write failed
// this run, plus a human-readable detail for the create summary. A
// struct (not a pre-formatted string) so the caller can both extend
// skipRepo with just the Repo name and print Detail in its own
// summary line, without re-parsing a combined string.
type envLocalFailure struct {
	Repo   string
	Detail string
}

func renderEnvLocals(
	stderr io.Writer,
	cfg *config.Config,
	worktreeRoot, name string,
	engines []engineInstance,
	portsCtx map[string]int,
	skip map[string]bool,
) []envLocalFailure {
	var failed []envLocalFailure
	for _, repo := range cfg.Repositories {
		if skip[repo.Name] {
			continue // repo did not materialise; no worktree to write into
		}
		if len(repo.EnvLocal) == 0 {
			continue
		}
		repoDst := filepath.Join(worktreeRoot, repo.Name)
		envCtx := envwriter.Context{
			Worktree:      envwriter.WorktreeCtx{Name: name, Root: worktreeRoot},
			Repo:          envwriter.RepoCtx{Name: repo.Name, Path: repoDst},
			Mysql:         engineContextFor("mysql", engines),
			Postgres:      engineContextFor("postgres", engines),
			Redis:         engineContextFor("redis", engines),
			Elasticsearch: engineContextFor("elasticsearch", engines),
			Ports:         portsCtx,
		}
		rendered, err := envwriter.Render(repo.EnvLocal, envCtx)
		if err != nil {
			// Best-effort, like runPostCreateHooks: a bad env_local template
			// must not abort the create before the worktree-path stdout emit +
			// failure summary. Record it and carry on.
			logf(stderr, "[bough] %s: env_local render failed: %v", repo.Name, err)
			failed = append(failed, envLocalFailure{Repo: repo.Name, Detail: fmt.Sprintf("render: %v", err)})
			continue
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
			logf(stderr, "[bough] %s: .env.local write failed: %v", repo.Name, err)
			failed = append(failed, envLocalFailure{Repo: repo.Name, Detail: fmt.Sprintf("write: %v", err)})
			continue
		}
		logf(stderr, "[bough] %s: .env.local written (%d keys)", repo.Name, len(rendered))
	}
	return failed
}

// runPostCreateHooks fires each repository's `post_create:` lines in
// declaration order. Failures log to stderr and the loop continues —
// a failed migration should not unwind the entire create, since the
// worktree materialisation itself is still valuable to the operator.
func runPostCreateHooks(ctx context.Context, stderr io.Writer, cfg *config.Config, worktreeRoot string, skip map[string]bool) []string {
	// Hook children must inherit the raw fd, not the SyncWriter: a
	// non-*os.File writer makes os/exec insert a pipe + copy goroutine
	// whose EOF only arrives once every inheriting descendant closes it
	// — a post_create that backgrounds a long-lived process would hang
	// create forever, and children would lose TTY-ness. No spinner is
	// active while hooks run, so direct fd writes cannot garble a
	// status line.
	hookOut := termio.ExecWriter(stderr)
	var failed []string
	for _, repo := range cfg.Repositories {
		if skip[repo.Name] {
			continue // repo did not materialise, or its env_local failed to render; nothing to run hooks against
		}
		if len(repo.PostCreate) == 0 {
			continue
		}
		repoDst := filepath.Join(worktreeRoot, repo.Name)
		envLocalPath := filepath.Join(repoDst, ".env.local")
		for _, line := range repo.PostCreate {
			logf(stderr, "[bough] %s post_create: %s", repo.Name, line)
			c := exec.CommandContext(ctx, "bash", "-c", line)
			c.Dir = repoDst
			// Re-read .env.local before every line rather than once per
			// repo: an earlier line may have derived and appended a new
			// value (the documented idiom this feature exists for, e.g.
			// composing a DSN from a port bough just allocated), and a
			// snapshot taken before the loop would starve every line
			// after the first of that value.
			c.Env = append(os.Environ(), parseEnvLocal(stderr, repo.Name, envLocalPath)...)
			c.Stdout = hookOut
			c.Stderr = hookOut
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

func toPluginSpecs(in []config.EnginePlugin) []engineapi.PluginSpec {
	out := make([]engineapi.PluginSpec, len(in))
	for i, p := range in {
		out[i] = engineapi.PluginSpec{ID: p.ID, Location: p.Location}
	}
	return out
}

// resolveRegistryPath picks the v0.11 canonical `.bough/ports.json`
// when it exists, then the pre-v0.11 `.bough-ports.json`, and finally
// whatever the YAML declared (typically v0.3's `.worktree-ports.json`).
// The host's registry loader auto-upgrades legacy keys in every case,
// so the operator can migrate at any pace.
func resolveRegistryPath(monorepoRoot, yamlPath string) string {
	// Preference order: the v0.11 `.bough/ports.json` (so a migrated
	// monorepo stops reading the flat file), then the pre-v0.11
	// `.bough-ports.json`, then whatever the operator wrote in
	// registry.path. The registry loader auto-upgrades legacy keys in
	// any case, so operators can migrate at their own pace.
	for _, rel := range []string{filepath.Join(boughDir, portsFile), registry.CanonicalPath} {
		p := filepath.Join(monorepoRoot, rel)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(monorepoRoot, yamlPath)
}

// parseEnvLocal reads a `.env.local` file and returns its `KEY=VALUE`
// lines verbatim so the caller can pass them through to a child
// process's exec.Cmd.Env. Comments (`#`-prefixed) and blank lines
// are skipped; lines without an `=` are silently dropped. A read
// failure other than the file simply not existing yet is logged
// (repoName identifies which repo's post_create is affected) rather
// than silently treated the same as "no .env.local".
//
// IMPORTANT: this parser does NOT do shell unquoting / interpolation
// of values. The bough envwriter emits raw values without surrounding
// quotes (matching the historical direnv `dotenv_if_exists .env.local`
// idiom) and bash `source` would choke on the `(`, `&`, `?` chars
// many DSNs / URLs contain — that exact failure mode is the bug this
// helper exists to bypass. This is a deliberate, tested contract (see
// TestParseEnvLocal_ValuesWithShellMetachars's WITH_QUOTES case): a
// value a post_create script appends with hand-written quotes is
// passed through with those quote characters intact, not stripped —
// stripping would be ambiguous with a value that genuinely needs a
// literal leading/trailing quote character, which this parser cannot
// distinguish without a real quoting format the .env.local file
// doesn't have (see issue #75 for the concrete failure case and
// why a fix needs a coordinated envwriter.Write format decision,
// not a parseEnvLocal-only change).
func parseEnvLocal(stderr io.Writer, repoName, path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logf(stderr, "[bough] %s: .env.local read failed: %v", repoName, err)
		}
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
