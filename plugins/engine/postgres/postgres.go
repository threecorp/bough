//go:build darwin || linux

// Package postgres implements the bough EngineProvider for PostgreSQL
// 16 via services-flake. The plugin binary spawned from
// cmd/bough-plugin-postgres/main.go wraps this Provider as a
// Hashicorp go-plugin gRPC server.
//
// Lifecycle parity with the bough-plugin-mysql sibling:
//
//	Up      → extract embedded flake to <worktree>/.local/bough-postgres-flake/
//	          → launch `nix run --impure 'path:<extracted>#postgres' -- up`
//	            detached (Setsid) so the hook returns before postgres is ready
//	ReadyCheck → poll `pg_isready -h 127.0.0.1 -p <port>` (falling back to
//	             a raw TCP probe if pg_isready is not on PATH) for up to
//	             timeoutSec seconds
//	Down    → graceful `nix run … -- down` with timeout
//	          → fallback lsof + SIGTERM → SIGKILL on the listening PID
//	          → reap stray process-compose supervisors whose cwd hangs
//	            off the worktree, otherwise the supervisor respawns
//	Cleanup → rm -rf <datadir>
//
// The plugin is darwin / linux only — Setsid lives on Unix and
// services-flake itself does not target Windows.
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"net"
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

// Provider implements api.EngineProvider for PostgreSQL 16 via
// services-flake. Construct via New() so any future tunables (alternate
// PortRange, custom flake ref, etc.) can be threaded as struct fields
// without breaking the constructor surface.
type Provider struct {
	FlakeRefOverride string
	PortLow          int
	PortHigh         int
}

// New returns a Provider with production defaults.
func New() *Provider { return &Provider{} }

const (
	// Range chosen to avoid the bough-plugin-mysql zone (42000-44999)
	// and any 33000-41999 used by prior bash-hook deployments.
	defaultPortLow      = 50000
	defaultPortHigh     = 52999
	defaultSocketDir    = "/tmp"
	flakeDirRelative    = ".local/bough-postgres-flake"
	startupLogRelative  = ".local/bough-postgres-startup.log"
	defaultGracefulSecs = 10
	socketPrefix        = "bough-postgres"
)

// Up extracts the embedded services-flake wrapper into the worktree
// and launches `nix run --impure '.#postgres' -- up --tui=false` as a
// detached subprocess.
//
// When `req.Extras["backend"] == "docker"` the Docker code path is
// used (docker.go) instead, bypassing the nix flake entirely.
func (p *Provider) Up(ctx context.Context, req *api.UpReq) error {
	if req.Extras["backend"] == "docker" {
		return p.dockerUp(ctx, req)
	}
	if req.WorktreeRoot == "" {
		return errors.New("postgres: Up: WorktreeRoot is required")
	}
	port := api.PickMainPort(req.Ports)
	if port <= 0 {
		return fmt.Errorf("postgres: Up: invalid port %d (Ports=%v)", port, req.Ports)
	}
	flakeDir := filepath.Join(req.WorktreeRoot, flakeDirRelative)
	if err := procutil.DeployFlake(nixAssets, "nix", flakeDir); err != nil {
		return fmt.Errorf("postgres: deploy flake: %w", err)
	}
	if req.Datadir != "" {
		// Only the parent dir — NOT req.Datadir itself. services-flake's
		// postgres setup script only runs initdb when `[[ ! -d "$PGDATA" ]]`
		// (unlike mysql, whose init-detection checks for a marker *file*
		// inside the dir and tolerates a pre-created empty one);
		// pre-creating $PGDATA here would defeat that check and initdb
		// would never run.
		if err := os.MkdirAll(filepath.Dir(req.Datadir), 0o755); err != nil {
			return fmt.Errorf("postgres: mkdir datadir parent: %w", err)
		}
	}
	flakeRef := p.flakeRef(flakeDir)
	logPath := filepath.Join(req.WorktreeRoot, startupLogRelative)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("postgres: mkdir log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("postgres: open log: %w", err)
	}
	// Detached via Setsid so the process outlives this call — but
	// exec.CommandContext arms a kill-on-ctx-done watchdog regardless
	// of Setsid, and ctx here is the per-RPC gRPC context, which
	// grpc-go cancels the instant Up() returns. Without
	// WithoutCancel, the watchdog SIGKILLs `nix run` microseconds
	// after Start(), long before flake evaluation finishes.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), "nix", "run", "--impure",
		flakeRef+"#postgres", "--", "up", "--tui=false")
	cmd.Dir = req.WorktreeRoot
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("BOUGH_POSTGRES_PORT=%d", port),
		fmt.Sprintf("BOUGH_POSTGRES_SOCKET_DIR=%s", socketDirOrDefault(req.SocketDir)),
		fmt.Sprintf("BOUGH_POSTGRES_DATADIR=%s", req.Datadir),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("postgres: nix run: %w", err)
	}
	// Reap on exit to avoid a zombie; Down() locates and signals the
	// real postgres/process-compose PID independently via lsof, so
	// this goroutine has nothing else to coordinate with.
	go func() { _ = cmd.Wait() }()
	_ = logFile.Close()
	return nil
}

// ReadyCheck polls for postgres connectivity on the main port. We
// prefer `pg_isready` (ships with services-flake's postgresql package)
// and fall back to a raw TCP probe so the host doesn't need psql on
// PATH for the readiness gate.
//
// Backend detection: a container named `bough-postgres-<port>` →
// docker path; otherwise the nix path.
func (p *Provider) ReadyCheck(ctx context.Context, ports []int, timeoutSec int) (bool, error) {
	port := firstListenPort(ports)
	if port <= 0 {
		return false, fmt.Errorf("postgres: ReadyCheck: invalid ports %v", ports)
	}
	if usingDockerBackend(ctx, port) {
		return p.dockerReadyCheck(ctx, port, timeoutSec)
	}
	if timeoutSec <= 0 {
		timeoutSec = 600
	}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	usePGIsReady := true
	if _, err := exec.LookPath("pg_isready"); err != nil {
		usePGIsReady = false
	}
	for time.Now().Before(deadline) {
		if usePGIsReady {
			c := exec.CommandContext(ctx, "pg_isready", "-h", "127.0.0.1",
				"-p", strconv.Itoa(port), "-q")
			if err := c.Run(); err == nil {
				return true, nil
			}
		} else {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				return true, nil
			}
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return false, fmt.Errorf("postgres: not ready on port %d within %ds", port, timeoutSec)
}

// Down attempts a graceful `nix run … -- down`, then reaps any PID
// still listening on the main port via lsof + SIGTERM/SIGKILL, then
// kills stray process-compose supervisors whose cwd lives under the
// worktree.
//
// Backend detection mirrors ReadyCheck.
func (p *Provider) Down(ctx context.Context, req *api.DownReq) error {
	port := firstListenPort(req.Ports)
	if usingDockerBackend(ctx, port) {
		return p.dockerDown(ctx, req)
	}
	if req.WorktreeRoot == "" {
		return errors.New("postgres: Down: WorktreeRoot is required")
	}
	timeout := time.Duration(req.GracefulTimeoutSec) * time.Second
	if req.GracefulTimeoutSec <= 0 {
		timeout = defaultGracefulSecs * time.Second
	}
	flakeDir := filepath.Join(req.WorktreeRoot, flakeDirRelative)
	if _, err := os.Stat(flakeDir); err == nil {
		gctx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(gctx, "nix", "run", "--impure",
			p.flakeRef(flakeDir)+"#postgres", "--", "down")
		cmd.Dir = req.WorktreeRoot
		cmd.Env = append(os.Environ(), fmt.Sprintf("BOUGH_POSTGRES_PORT=%d", port))
		_ = cmd.Run()
		cancel()
	}
	if pid := procutil.LsofListener(port); pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		time.Sleep(time.Second)
		if again := procutil.LsofListener(port); again == pid {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	procutil.KillStrayProcessCompose(req.WorktreeRoot)
	return nil
}

// Cleanup removes the postgres datadir. Down must have already
// converged on "nothing listening on Port".
func (p *Provider) Cleanup(_ context.Context, datadir string, _ []int) error {
	if datadir == "" {
		return errors.New("postgres: Cleanup: datadir is required")
	}
	return os.RemoveAll(datadir)
}

// PortRangeDefault returns the plugin's recommended port range under
// role "main" (the only role this single-port engine uses).
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

// EnvVars exposes the per-worktree connection coordinates. Consumers
// build their DSN from these in the YAML template (the actual DSN is
// monorepo-specific because the user / database name vary).
func (p *Provider) EnvVars(_ context.Context, req *api.EnvVarsReq) (map[string]string, error) {
	port := api.PickMainPort(req.Ports)
	dir := socketDirOrDefault(req.SocketDir)
	return map[string]string{
		"BOUGH_POSTGRES_HOST":       "127.0.0.1",
		"BOUGH_POSTGRES_PORT":       strconv.Itoa(port),
		"BOUGH_POSTGRES_SOCKET_DIR": dir,
	}, nil
}

// firstListenPort returns ports[0], or 0 when ports is empty.
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

// socketPrefix is referenced by tests asserting on the socket-dir
// convention; keep it exported within-package so the test file can
// pin the constant alongside the impl.
var _ = socketPrefix
