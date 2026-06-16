//go:build darwin || linux

// Package mysql implements the bough DBProvider for MySQL 8.4 LTS via
// services-flake. The plugin binary spawned from
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
	"bufio"
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/db/api"
)

//go:embed nix
var nixAssets embed.FS

// Provider implements api.DBProvider for MySQL 8.4 LTS via
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
func (p *Provider) Up(ctx context.Context, req api.UpReq) error {
	if req.Extras["backend"] == "docker" {
		return p.dockerUp(ctx, req)
	}
	if req.WorktreeRoot == "" {
		return errors.New("mysql: Up: WorktreeRoot is required")
	}
	if req.Port <= 0 {
		return fmt.Errorf("mysql: Up: invalid port %d", req.Port)
	}
	flakeDir := filepath.Join(req.WorktreeRoot, flakeDirRelative)
	if err := deployFlake(flakeDir); err != nil {
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
	cmd := exec.CommandContext(ctx, "nix", "run", "--impure",
		flakeRef+"#mysql", "--", "up", "--tui=false")
	cmd.Dir = req.WorktreeRoot
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("BOUGH_MYSQL_PORT=%d", req.Port),
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
	// Hand the log fd off to the subprocess; closing here would not
	// affect the dup the subprocess holds.
	_ = logFile.Close()
	return nil
}

// ReadyCheck polls for mysql connectivity on `port` for up to
// `timeoutSec` seconds. Returns ready=true on the first successful
// `SELECT 1`, otherwise (false, error) at deadline.
//
// Backend detection: if a container named `bough-mysql-<port>` exists,
// the Docker path is taken; otherwise we fall through to the nix-flake
// path. ReadyCheckReq has no `backend` field today so this is the
// cheapest way to stay backward-compatible while honouring Up's choice.
func (p *Provider) ReadyCheck(ctx context.Context, port, timeoutSec int) (bool, error) {
	if port <= 0 {
		return false, fmt.Errorf("mysql: ReadyCheck: invalid port %d", port)
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
// reaps any PID still listening on `Port` via lsof + SIGTERM/SIGKILL.
// Finally it kills stray process-compose supervisors whose cwd lives
// under the worktree (those supervisors respawn mysqld immediately
// after a SIGKILL otherwise).
//
// Backend detection mirrors ReadyCheck: if a docker container named
// `bough-mysql-<port>` exists, dockerDown is invoked instead.
func (p *Provider) Down(ctx context.Context, req api.DownReq) error {
	if usingDockerBackend(ctx, req.Port) {
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
		cmd.Env = append(os.Environ(), fmt.Sprintf("BOUGH_MYSQL_PORT=%d", req.Port))
		// Graceful frequently fails because services-flake's
		// --tui=false runs process-compose without its HTTP API.
		// That's fine: the lsof fallback below handles it.
		_ = cmd.Run()
		cancel()
	}
	if pid := lsofListener(req.Port); pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		time.Sleep(time.Second)
		// Re-check before SIGKILL to avoid sending it to an unrelated
		// PID that may have grabbed the port in the interim.
		if again := lsofListener(req.Port); again == pid {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	killStrayProcessCompose(req.WorktreeRoot)
	return nil
}

// Cleanup removes the mysqld datadir. Down must have already converged
// on "nothing listening on Port"; calling Cleanup with mysqld still
// alive would delete the datadir under an open mysqld and crash it.
func (p *Provider) Cleanup(_ context.Context, datadir string, _ int) error {
	if datadir == "" {
		return errors.New("mysql: Cleanup: datadir is required")
	}
	return os.RemoveAll(datadir)
}

// PortRangeDefault returns the plugin's recommended port range. Used
// by the host's `bough plugins list` and as the YAML default when
// `databases[].port_range` is empty.
func (p *Provider) PortRangeDefault(_ context.Context) (int, int, error) {
	low := p.PortLow
	high := p.PortHigh
	if low == 0 {
		low = defaultPortLow
	}
	if high == 0 {
		high = defaultPortHigh
	}
	return low, high, nil
}

// EnvVars exposes the per-worktree connection coordinates to the host
// so the host can render BOUGH_MYSQL_* into every .env.local that
// declares them in its YAML template.
func (p *Provider) EnvVars(_ context.Context, req api.EnvVarsReq) (map[string]string, error) {
	dir := socketDirOrDefault(req.SocketDir)
	return map[string]string{
		"BOUGH_MYSQL_HOST":   "127.0.0.1",
		"BOUGH_MYSQL_PORT":   strconv.Itoa(req.Port),
		"BOUGH_MYSQL_SOCKET": filepath.Join(dir, fmt.Sprintf("%s-%d.sock", socketPrefix, req.Port)),
	}, nil
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

// deployFlake materialises the embedded `nix/` directory under dst.
// Re-running is idempotent: existing files are overwritten so a future
// plugin upgrade picks up the new wrapper without manual cleanup.
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

// lsofListener returns the PID of whichever process holds the TCP
// listener on `port`, or 0 when nothing is listening. lsof's `-t`
// flag prints PIDs only, one per line; we take the first.
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

// killStrayProcessCompose SIGTERM-s any process-compose subprocess whose
// cwd lives under `cwdPrefix`. Without this step the supervisor would
// respawn mysqld immediately after a SIGKILL.
//
// macOS lsof requires `-a` (AND semantics) when combining `-p` with
// other filters; without it lsof joins the filters with OR and the
// caller risks killing unrelated supervisors. We only need cwd here
// so a bare `lsof -p PID` is sufficient.
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
				if strings.HasPrefix(fields[len(fields)-1], cwdPrefix) {
					_ = syscall.Kill(pid, syscall.SIGTERM)
				}
				break
			}
		}
	}
}
