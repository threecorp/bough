//go:build darwin || linux

// Package redis implements the bough EngineProvider for Redis 7 via
// services-flake. The plugin binary spawned from
// cmd/bough-plugin-redis/main.go wraps this Provider as a Hashicorp
// go-plugin gRPC server.
//
// Lifecycle parity with the bough-plugin-mysql / postgres siblings:
//
//	Up         → extract embedded flake to <worktree>/.local/bough-redis-flake/
//	             → launch `nix run --impure '.#redis' -- up --tui=false`
//	               detached via Setsid
//	ReadyCheck → poll `redis-cli -h 127.0.0.1 -p <port> PING` (TCP probe
//	             fallback when redis-cli is not on PATH)
//	Down       → graceful `nix run … -- down` then lsof + SIGTERM/SIGKILL
//	             + stray process-compose supervisor cleanup
//	Cleanup    → rm -rf <datadir>
//	PortRange  → 53000-55999 (out of mysql 42000-44999, postgres
//	             50000-52999, prior bash-hook 33000-41999)
//	EnvVars    → BOUGH_REDIS_HOST / _PORT / _URL (the URL is the
//	             redis://… form most clients accept verbatim)
//
// darwin / linux only — Setsid is Unix and services-flake itself
// does not target Windows.
package redis

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"
)

//go:embed nix
var nixAssets embed.FS

// Provider implements api.EngineProvider for Redis 7 via services-flake.
type Provider struct {
	FlakeRefOverride string
	PortLow          int
	PortHigh         int
}

// New returns a Provider with production defaults.
func New() *Provider { return &Provider{} }

const (
	defaultPortLow      = 53000
	defaultPortHigh     = 55999
	flakeDirRelative    = ".local/bough-redis-flake"
	startupLogRelative  = ".local/bough-redis-startup.log"
	defaultGracefulSecs = 10
)

// Up extracts the embedded services-flake wrapper into the worktree
// and launches `nix run --impure '.#redis' -- up --tui=false` as a
// detached subprocess.
//
// When `req.Extras["backend"] == "docker"` the Docker code path
// (docker.go) is used instead.
func (p *Provider) Up(ctx context.Context, req *api.UpReq) error {
	if req.Extras["backend"] == "docker" {
		return p.dockerUp(ctx, req)
	}
	if req.WorktreeRoot == "" {
		return errors.New("redis: Up: WorktreeRoot is required")
	}
	port := api.PickMainPort(req.Ports)
	if port <= 0 {
		return fmt.Errorf("redis: Up: invalid port %d (Ports=%v)", port, req.Ports)
	}
	flakeDir := filepath.Join(req.WorktreeRoot, flakeDirRelative)
	if err := deployFlake(flakeDir); err != nil {
		return fmt.Errorf("redis: deploy flake: %w", err)
	}
	if req.Datadir != "" {
		if err := os.MkdirAll(req.Datadir, 0o755); err != nil {
			return fmt.Errorf("redis: mkdir datadir: %w", err)
		}
	}
	flakeRef := p.flakeRef(flakeDir)
	logPath := filepath.Join(req.WorktreeRoot, startupLogRelative)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("redis: mkdir log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("redis: open log: %w", err)
	}
	// Detached via Setsid so the process outlives this call — but
	// exec.CommandContext arms a kill-on-ctx-done watchdog regardless
	// of Setsid, and ctx here is the per-RPC gRPC context, which
	// grpc-go cancels the instant Up() returns. Without
	// WithoutCancel, the watchdog SIGKILLs `nix run` microseconds
	// after Start(), long before flake evaluation finishes.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), "nix", "run", "--impure",
		flakeRef+"#redis", "--", "up", "--tui=false")
	cmd.Dir = req.WorktreeRoot
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("BOUGH_REDIS_PORT=%d", port),
		fmt.Sprintf("BOUGH_REDIS_DATADIR=%s", req.Datadir),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("redis: nix run: %w", err)
	}
	// Reap on exit to avoid a zombie; Down() locates and signals the
	// real redis/process-compose PID independently via lsof, so this
	// goroutine has nothing else to coordinate with.
	go func() { _ = cmd.Wait() }()
	_ = logFile.Close()
	return nil
}

// ReadyCheck prefers `redis-cli PING` (the canonical liveness probe)
// and falls back to a raw TCP probe so the host doesn't need
// redis-cli on PATH for the readiness gate.
//
// Backend detection: a container named `bough-redis-<port>` → docker
// path; otherwise the nix path.
func (p *Provider) ReadyCheck(ctx context.Context, ports []int, timeoutSec int) (bool, error) {
	port := firstListenPort(ports)
	if port <= 0 {
		return false, fmt.Errorf("redis: ReadyCheck: invalid ports %v", ports)
	}
	if usingDockerBackend(ctx, port) {
		return p.dockerReadyCheck(ctx, port, timeoutSec)
	}
	if timeoutSec <= 0 {
		timeoutSec = 600
	}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	useRedisCLI := true
	if _, err := exec.LookPath("redis-cli"); err != nil {
		useRedisCLI = false
	}
	for time.Now().Before(deadline) {
		if useRedisCLI {
			c := exec.CommandContext(ctx, "redis-cli", "-h", "127.0.0.1",
				"-p", strconv.Itoa(port), "PING")
			out, err := c.Output()
			if err == nil && strings.TrimSpace(string(out)) == "PONG" {
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
	return false, fmt.Errorf("redis: not ready on port %d within %ds", port, timeoutSec)
}

// Down attempts a graceful `nix run … -- down`, then reaps any PID
// still listening on the main port, then kills stray process-compose
// supervisors whose cwd lives under the worktree.
//
// Backend detection mirrors ReadyCheck.
func (p *Provider) Down(ctx context.Context, req *api.DownReq) error {
	port := firstListenPort(req.Ports)
	if usingDockerBackend(ctx, port) {
		return p.dockerDown(ctx, req)
	}
	if req.WorktreeRoot == "" {
		return errors.New("redis: Down: WorktreeRoot is required")
	}
	timeout := time.Duration(req.GracefulTimeoutSec) * time.Second
	if req.GracefulTimeoutSec <= 0 {
		timeout = defaultGracefulSecs * time.Second
	}
	flakeDir := filepath.Join(req.WorktreeRoot, flakeDirRelative)
	if _, err := os.Stat(flakeDir); err == nil {
		gctx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(gctx, "nix", "run", "--impure",
			p.flakeRef(flakeDir)+"#redis", "--", "down")
		cmd.Dir = req.WorktreeRoot
		cmd.Env = append(os.Environ(), fmt.Sprintf("BOUGH_REDIS_PORT=%d", port))
		_ = cmd.Run()
		cancel()
	}
	if pid := lsofListener(port); pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		time.Sleep(time.Second)
		if again := lsofListener(port); again == pid {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	killStrayProcessCompose(req.WorktreeRoot)
	return nil
}

// Cleanup removes the redis datadir.
func (p *Provider) Cleanup(_ context.Context, datadir string, _ []int) error {
	if datadir == "" {
		return errors.New("redis: Cleanup: datadir is required")
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

// EnvVars exposes the per-worktree connection coordinates. The URL is
// the redis://… form most language SDKs (go-redis, redis-py, ioredis)
// accept directly without further parsing.
func (p *Provider) EnvVars(_ context.Context, req *api.EnvVarsReq) (map[string]string, error) {
	port := api.PickMainPort(req.Ports)
	return map[string]string{
		"BOUGH_REDIS_HOST": "127.0.0.1",
		"BOUGH_REDIS_PORT": strconv.Itoa(port),
		"BOUGH_REDIS_URL":  fmt.Sprintf("redis://127.0.0.1:%d/0", port),
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

func deployFlake(dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return fs.WalkDir(nixAssets, "nix", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("nix", p)
		if rel == "" || rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(nixAssets, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func lsofListener(port int) int {
	out, err := exec.Command("lsof", fmt.Sprintf("-tiTCP:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0
	}
	if i := strings.IndexAny(s, "\n\t "); i >= 0 {
		s = s[:i]
	}
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return pid
}

func killStrayProcessCompose(cwdPrefix string) {
	out, err := exec.Command("pgrep", "-f", "process-compose").Output()
	if err != nil {
		return
	}
	for _, ps := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(ps)
		if err != nil {
			continue
		}
		cwdOut, err := exec.Command("lsof", "-p", ps).Output()
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(cwdOut))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 9 && fields[3] == "cwd" {
				// Exact match or a real path-separator boundary — a bare
				// HasPrefix would also match ".../auba-api-1394" against
				// cwdPrefix ".../auba-api-139", SIGTERMing a sibling
				// worktree's still-running supervisor.
				if cwd := fields[len(fields)-1]; cwd == cwdPrefix || strings.HasPrefix(cwd, cwdPrefix+"/") {
					_ = syscall.Kill(pid, syscall.SIGTERM)
				}
				break
			}
		}
	}
}
