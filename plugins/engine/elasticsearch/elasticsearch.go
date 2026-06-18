//go:build darwin || linux

// Package elasticsearch implements the bough EngineProvider for
// Elasticsearch 7.x via a custom process-compose entry (services-flake
// does not ship a built-in elasticsearch module). The plugin binary
// spawned from cmd/bough-plugin-elasticsearch/main.go wraps this
// Provider as a Hashicorp go-plugin gRPC server.
//
// Lifecycle:
//
//	Up         → extract embedded flake to <worktree>/.local/
//	             bough-elasticsearch-flake/ → launch
//	             `nix run --impure '.#elasticsearch' -- up --tui=false`
//	             detached via Setsid
//	ReadyCheck → poll `curl -sf
//	             http://127.0.0.1:<port>/_cluster/health?wait_for_status=yellow`
//	             so a half-up node (JVM started, cluster forming) fails
//	             the probe until the green/yellow state is reached
//	Down       → graceful `nix run … -- down` then lsof + SIGTERM/SIGKILL
//	             + stray process-compose supervisor cleanup
//	Cleanup    → rm -rf <datadir>
//	PortRange  → 56000-58999 (out of mysql 42000-44999, postgres
//	             50000-52999, redis 53000-55999, prior bash-hook
//	             33000-41999)
//	EnvVars    → BOUGH_ELASTICSEARCH_HOST / _PORT / _URL
//
// Cold-start budget is ~30-60s (JVM + index recovery on a fresh data
// directory); warm-start is ~10-20s. Operators should run the host's
// ReadyCheck with timeoutSec >= 600 for cold scenarios.
package elasticsearch

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
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

// Provider implements api.EngineProvider for Elasticsearch 7.x via the
// embedded process-compose-flake wrapper.
type Provider struct {
	FlakeRefOverride string
	PortLow          int
	PortHigh         int
}

// New returns a Provider with production defaults.
func New() *Provider { return &Provider{} }

const (
	defaultPortLow      = 56000
	defaultPortHigh     = 58999
	flakeDirRelative    = ".local/bough-elasticsearch-flake"
	startupLogRelative  = ".local/bough-elasticsearch-startup.log"
	defaultGracefulSecs = 15 // ES shutdown is slower than mysql / redis
)

// Up extracts the embedded flake and launches Elasticsearch via
// process-compose, detached so the WorktreeCreate hook returns before
// the JVM has finished warming up. ReadyCheck is the host's gate.
//
// When `req.Extras["backend"] == "docker"` the Docker code path
// (docker.go) is used instead.
func (p *Provider) Up(ctx context.Context, req *api.UpReq) error {
	if req.Extras["backend"] == "docker" {
		return p.dockerUp(ctx, req)
	}
	if req.WorktreeRoot == "" {
		return errors.New("elasticsearch: Up: WorktreeRoot is required")
	}
	port := api.PickMainPort(req.Ports)
	if port <= 0 {
		return fmt.Errorf("elasticsearch: Up: invalid port %d (Ports=%v)", port, req.Ports)
	}
	flakeDir := filepath.Join(req.WorktreeRoot, flakeDirRelative)
	if err := deployFlake(flakeDir); err != nil {
		return fmt.Errorf("elasticsearch: deploy flake: %w", err)
	}
	if req.Datadir != "" {
		if err := os.MkdirAll(req.Datadir, 0o755); err != nil {
			return fmt.Errorf("elasticsearch: mkdir datadir: %w", err)
		}
	}
	flakeRef := p.flakeRef(flakeDir)
	logPath := filepath.Join(req.WorktreeRoot, startupLogRelative)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("elasticsearch: mkdir log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("elasticsearch: open log: %w", err)
	}
	heap := req.Extras["heap"]
	if heap == "" {
		heap = "1g"
	}
	cmd := exec.CommandContext(ctx, "nix", "run", "--impure",
		flakeRef+"#elasticsearch", "--", "up", "--tui=false")
	cmd.Dir = req.WorktreeRoot
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("BOUGH_ELASTICSEARCH_PORT=%d", port),
		fmt.Sprintf("BOUGH_ELASTICSEARCH_DATADIR=%s", req.Datadir),
		fmt.Sprintf("BOUGH_ELASTICSEARCH_HEAP=%s", heap),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("elasticsearch: nix run: %w", err)
	}
	_ = logFile.Close()
	return nil
}

// ReadyCheck polls the cluster-health endpoint until ES reports a
// yellow (or green) status. A bare TCP connect would succeed too early
// — the JVM listens on the port before the cluster has finished
// forming, and an early-bird client query returns 503. The
// cluster-health endpoint is the canonical liveness probe.
//
// Backend detection: a container named `bough-elasticsearch-<port>` →
// docker path; otherwise the nix path.
func (p *Provider) ReadyCheck(ctx context.Context, ports []int, timeoutSec int) (bool, error) {
	port := firstListenPort(ports)
	if port <= 0 {
		return false, fmt.Errorf("elasticsearch: ReadyCheck: invalid ports %v", ports)
	}
	if usingDockerBackend(ctx, port) {
		return p.dockerReadyCheck(ctx, port, timeoutSec)
	}
	if timeoutSec <= 0 {
		timeoutSec = 600
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/_cluster/health?wait_for_status=yellow&timeout=5s", port)
	httpClient := &http.Client{Timeout: 6 * time.Second}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		// TCP probe first so we skip the HTTP roundtrip while the JVM
		// is still booting (the dial fails fast then; the HTTP path
		// would block for several seconds before timing out).
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if dialErr != nil {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		_ = conn.Close()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true, nil
			}
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return false, fmt.Errorf("elasticsearch: not ready on port %d within %ds", port, timeoutSec)
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
		return errors.New("elasticsearch: Down: WorktreeRoot is required")
	}
	timeout := time.Duration(req.GracefulTimeoutSec) * time.Second
	if req.GracefulTimeoutSec <= 0 {
		timeout = defaultGracefulSecs * time.Second
	}
	flakeDir := filepath.Join(req.WorktreeRoot, flakeDirRelative)
	if _, err := os.Stat(flakeDir); err == nil {
		gctx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(gctx, "nix", "run", "--impure",
			p.flakeRef(flakeDir)+"#elasticsearch", "--", "down")
		cmd.Dir = req.WorktreeRoot
		cmd.Env = append(os.Environ(), fmt.Sprintf("BOUGH_ELASTICSEARCH_PORT=%d", port))
		_ = cmd.Run()
		cancel()
	}
	if pid := lsofListener(port); pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		time.Sleep(2 * time.Second) // ES needs a moment to flush translogs
		if again := lsofListener(port); again == pid {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	killStrayProcessCompose(req.WorktreeRoot)
	return nil
}

// Cleanup removes the elasticsearch datadir.
func (p *Provider) Cleanup(_ context.Context, datadir string, _ []int) error {
	if datadir == "" {
		return errors.New("elasticsearch: Cleanup: datadir is required")
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
// the http://… form most Elasticsearch SDKs (go-elasticsearch,
// elasticsearch-py, @elastic/elasticsearch) accept verbatim.
func (p *Provider) EnvVars(_ context.Context, req *api.EnvVarsReq) (map[string]string, error) {
	port := api.PickMainPort(req.Ports)
	return map[string]string{
		"BOUGH_ELASTICSEARCH_HOST": "127.0.0.1",
		"BOUGH_ELASTICSEARCH_PORT": strconv.Itoa(port),
		"BOUGH_ELASTICSEARCH_URL":  fmt.Sprintf("http://127.0.0.1:%d", port),
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
				if strings.HasPrefix(fields[len(fields)-1], cwdPrefix) {
					_ = syscall.Kill(pid, syscall.SIGTERM)
				}
				break
			}
		}
	}
}
