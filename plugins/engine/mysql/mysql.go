//go:build darwin || linux

// Package mysql implements the bough EngineProvider for MySQL 8.4 LTS
// via services-flake. The plugin binary spawned from
// cmd/bough-plugin-mysql/main.go wraps this Provider as a Hashicorp
// go-plugin gRPC server.
//
// Lifecycle:
//
//	Up      → extract embedded flake to <worktree>/.local/bough-mysql-flake/
//	          → launch `nix run --impure 'path:<extracted>#mysql' -- up`
//	            detached (Setsid) so the hook returns before mysqld is ready
//	ReadyCheck → poll `mysql -uroot -h127.0.0.1 -P<port> -e 'SELECT 1'`
//	             for up to timeoutSec seconds
//	Down    → graceful `nix run … -- down` with timeout
//	          → fallback lsof + SIGTERM → SIGKILL on the listening PID
//	          → reap stray process-compose supervisors whose cwd hangs
//	            off the worktree, otherwise the supervisor respawns mysqld
//	Cleanup → rm -rf <datadir>
//
// The plugin is darwin / linux only — Setsid lives on Unix and
// services-flake itself does not target Windows.
package mysql

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"

	"github.com/ikeikeikeike/bough/pkg/procutil"
)

//go:embed nix
var nixAssets embed.FS

// Provider implements api.EngineProvider for MySQL 8.4 LTS via
// services-flake. Construct via New() so any future tunables (alternate
// PortRange, custom flake ref, etc.) can be threaded as struct fields
// without breaking the constructor surface.
type Provider struct {
	// FlakeRefOverride lets tests redirect the `nix run` invocation to
	// a fake flake (e.g. a script that exits 0 immediately) without
	// having to install services-flake. Production callers leave this
	// empty and the Up path uses the worktree-local extracted flake.
	FlakeRefOverride string

	// PortLow / PortHigh override PortRangeDefault. Production callers
	// leave them zero and the defaults (42000, 44999) take effect.
	PortLow  int
	PortHigh int
}

// New returns a Provider with production defaults.
func New() *Provider { return &Provider{} }

const (
	defaultPortLow      = 42000
	defaultPortHigh     = 44999
	defaultSocketDir    = "/tmp"
	flakeDirRelative    = ".local/bough-mysql-flake"
	startupLogRelative  = ".local/bough-mysql-startup.log"
	defaultGracefulSecs = 10
	socketPrefix        = "bough-mysql"
)

// Up extracts the embedded services-flake wrapper into the worktree
// and launches `nix run --impure '.#mysql' -- up --tui=false` as a
// detached subprocess. The subprocess survives until Down (or an lsof
// kill) terminates it.
//
// When `req.Extras["backend"] == "docker"` the Docker-backed code path
// (docker.go) is used instead, bypassing the nix flake entirely.
func (p *Provider) Up(ctx context.Context, req *api.UpReq) error {
	if req.Extras["backend"] == "docker" {
		return p.dockerUp(ctx, req)
	}
	if req.WorktreeRoot == "" {
		return errors.New("mysql: Up: WorktreeRoot is required")
	}
	port := api.PickMainPort(req.Ports)
	if port <= 0 {
		return fmt.Errorf("mysql: Up: invalid port %d (Ports=%v)", port, req.Ports)
	}
	flakeDir := filepath.Join(req.WorktreeRoot, flakeDirRelative)
	if err := procutil.DeployFlake(nixAssets, "nix", flakeDir); err != nil {
		return fmt.Errorf("mysql: deploy flake: %w", err)
	}
	if req.Datadir != "" {
		if err := os.MkdirAll(req.Datadir, 0o755); err != nil {
			return fmt.Errorf("mysql: mkdir datadir: %w", err)
		}
	}
	flakeRef := p.flakeRef(flakeDir)
	logPath := filepath.Join(req.WorktreeRoot, startupLogRelative)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("mysql: mkdir log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("mysql: open log: %w", err)
	}
	// Detached via Setsid so the process outlives this call — but
	// exec.CommandContext arms a kill-on-ctx-done watchdog regardless
	// of Setsid, and ctx here is the per-RPC gRPC context, which
	// grpc-go cancels the instant Up() returns. Without
	// WithoutCancel, the watchdog SIGKILLs `nix run` microseconds
	// after Start(), long before flake evaluation finishes.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), "nix", "run", "--impure",
		flakeRef+"#mysql", "--", "up", "--tui=false")
	cmd.Dir = req.WorktreeRoot
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("BOUGH_MYSQL_PORT=%d", port),
		fmt.Sprintf("BOUGH_MYSQL_SOCKET_DIR=%s", socketDirOrDefault(req.SocketDir)),
		fmt.Sprintf("BOUGH_MYSQL_DATADIR=%s", req.Datadir),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Detach so the WorktreeCreate hook can return without waiting for
	// mysqld to become ready. ReadyCheck is the host's "is it up yet?"
	// poll.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("mysql: nix run: %w", err)
	}
	// Reap on exit to avoid a zombie; Down() locates and signals the
	// real mysqld/process-compose PID independently via lsof, so this
	// goroutine has nothing else to coordinate with.
	go func() { _ = cmd.Wait() }()
	// Hand the log fd off to the subprocess; closing here would not
	// affect the dup the subprocess holds.
	_ = logFile.Close()
	return nil
}

// ReadyCheck polls for mysql connectivity on the main port for up to
// `timeoutSec` seconds. Returns ready=true on the first successful
// `SELECT 1`, otherwise (false, error) at deadline.
//
// The single-port mysql plugin reads ports[0] (the host orders entries
// to match the PortRangeDefault keys; "main" is always first). Backend
// detection: if a container named `bough-mysql-<port>` exists, the
// Docker path is taken; otherwise we fall through to the nix-flake
// path.
func (p *Provider) ReadyCheck(ctx context.Context, ports []int, timeoutSec int) (bool, error) {
	port := firstListenPort(ports)
	if port <= 0 {
		return false, fmt.Errorf("mysql: ReadyCheck: invalid ports %v", ports)
	}
	if usingDockerBackend(ctx, port) {
		return p.dockerReadyCheck(ctx, port, timeoutSec)
	}
	if timeoutSec <= 0 {
		timeoutSec = 600
	}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		c := exec.CommandContext(ctx, "mysql", "-uroot", "-h127.0.0.1",
			fmt.Sprintf("-P%d", port), "-e", "SELECT 1")
		if err := c.Run(); err == nil {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return false, fmt.Errorf("mysql: not ready on port %d within %ds", port, timeoutSec)
}

// Down attempts a graceful `nix run … -- down`, then unconditionally
// reaps any PID still listening on the main port via lsof + SIGTERM/
// SIGKILL. Finally it kills stray process-compose supervisors whose
// cwd lives under the worktree (those supervisors respawn mysqld
// immediately after a SIGKILL otherwise).
//
// Backend detection mirrors ReadyCheck: if a docker container named
// `bough-mysql-<port>` exists, dockerDown is invoked instead.
func (p *Provider) Down(ctx context.Context, req *api.DownReq) error {
	port := firstListenPort(req.Ports)
	if usingDockerBackend(ctx, port) {
		return p.dockerDown(ctx, req)
	}
	if req.WorktreeRoot == "" {
		return errors.New("mysql: Down: WorktreeRoot is required")
	}
	timeout := time.Duration(req.GracefulTimeoutSec) * time.Second
	if req.GracefulTimeoutSec <= 0 {
		timeout = defaultGracefulSecs * time.Second
	}
	flakeDir := filepath.Join(req.WorktreeRoot, flakeDirRelative)
	if _, err := os.Stat(flakeDir); err == nil {
		gctx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(gctx, "nix", "run", "--impure",
			p.flakeRef(flakeDir)+"#mysql", "--", "down")
		cmd.Dir = req.WorktreeRoot
		cmd.Env = append(os.Environ(), fmt.Sprintf("BOUGH_MYSQL_PORT=%d", port))
		// Graceful frequently fails because services-flake's
		// --tui=false runs process-compose without its HTTP API.
		// That's fine: the lsof fallback below handles it.
		_ = cmd.Run()
		cancel()
	}
	if pid := procutil.LsofListener(port); pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		time.Sleep(time.Second)
		// Re-check before SIGKILL to avoid sending it to an unrelated
		// PID that may have grabbed the port in the interim.
		if again := procutil.LsofListener(port); again == pid {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	procutil.KillStrayProcessCompose(req.WorktreeRoot)
	return nil
}

// Cleanup removes the mysqld datadir. Down must have already converged
// on "nothing listening on Port"; calling Cleanup with mysqld still
// alive would delete the datadir under an open mysqld and crash it.
func (p *Provider) Cleanup(_ context.Context, datadir string, _ []int) error {
	if datadir == "" {
		return errors.New("mysql: Cleanup: datadir is required")
	}
	return os.RemoveAll(datadir)
}

// PortRangeDefault returns the plugin's recommended port range under
// role "main" (the only role this single-port engine uses). Used by
// the host's `bough plugins list` and as the YAML default when
// `engines[].port_ranges` is empty.
func (p *Provider) PortRangeDefault(_ context.Context) (map[string]api.PortRange, error) {
	low := p.PortLow
	high := p.PortHigh
	if low == 0 {
		low = defaultPortLow
	}
	if high == 0 {
		high = defaultPortHigh
	}
	return map[string]api.PortRange{"main": {Low: low, High: high}}, nil
}

// EnvVars exposes the per-worktree connection coordinates to the host
// so the host can render BOUGH_MYSQL_* into every .env.local that
// declares them in its YAML template.
func (p *Provider) EnvVars(_ context.Context, req *api.EnvVarsReq) (map[string]string, error) {
	port := api.PickMainPort(req.Ports)
	dir := socketDirOrDefault(req.SocketDir)
	return map[string]string{
		"BOUGH_MYSQL_HOST":   "127.0.0.1",
		"BOUGH_MYSQL_PORT":   strconv.Itoa(port),
		"BOUGH_MYSQL_SOCKET": filepath.Join(dir, fmt.Sprintf("%s-%d.sock", socketPrefix, port)),
	}, nil
}

// firstListenPort returns ports[0] (the host orders entries so the
// main role is first for single-port engines), or 0 when ports is
// empty. Kept as a tiny helper so the call sites read uniformly.
func firstListenPort(ports []int) int {
	if len(ports) > 0 {
		return ports[0]
	}
	return 0
}

func (p *Provider) flakeRef(extracted string) string {
	if p.FlakeRefOverride != "" {
		return p.FlakeRefOverride
	}
	return "path:" + extracted
}

func socketDirOrDefault(s string) string {
	if s == "" {
		return defaultSocketDir
	}
	return s
}
