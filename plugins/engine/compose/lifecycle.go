//go:build darwin || linux

package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"

	"github.com/ikeikeikeike/bough/pkg/dockerutil"
)

// composeURLSchemes maps a well-known compose service name to the URL
// scheme EnvVars advertises for it. An unrecognized service only gets
// _HOST/_PORT — guessing a scheme for an arbitrary wrapped service
// would be more likely wrong than absent.
var composeURLSchemes = map[string]string{
	"redis":      "redis",
	"postgres":   "postgresql",
	"postgresql": "postgresql",
	"mysql":      "mysql",
	"mongo":      "mongodb",
	"mongodb":    "mongodb",
}

// Up renders a worktree-scoped override (see renderOverride) and runs
// `docker compose -f <file> -f <override> -p <project> up -d
// <service>`, then verifies the container actually published the
// host-allocated port before persisting sidecar state for Down/
// Cleanup and caching it in-process for EnvVars/ReadyCheck.
//
// compose.file resolves relative to the RAW worktree root (the
// directory containing every declared repository as a sibling) —
// req.WorktreeRoot itself is the engine-provider repo's own worktree
// path (create.go's engineProviderWorktree), one level too deep for a
// path like "auba-api/compose.yml" that names a sibling repo. This is
// a deliberate deviation from how the other four plugins use
// WorktreeRoot (as their own base directory).
func (p *Provider) Up(ctx context.Context, req *api.UpReq) error {
	if req.WorktreeRoot == "" {
		return errors.New("compose: Up: WorktreeRoot is required")
	}
	port := api.PickMainPort(req.Ports)
	if port <= 0 {
		return fmt.Errorf("compose: Up: invalid port %d (Ports=%v)", port, req.Ports)
	}

	file := req.Extras["compose.file"]
	service := req.Extras["compose.service"]
	targetPortStr := req.Extras["compose.target_port"]
	if file == "" || service == "" || targetPortStr == "" {
		return errors.New("compose: Up: extras compose.file, compose.service, and compose.target_port are all required")
	}
	targetPort, err := strconv.Atoi(targetPortStr)
	if err != nil || targetPort <= 0 {
		return fmt.Errorf("compose: Up: invalid compose.target_port %q", targetPortStr)
	}

	worktreeName := filepath.Base(filepath.Dir(req.WorktreeRoot))
	composeFile := api.ResolveUnderRawWorktreeRoot(req.WorktreeRoot, file)
	if _, err := os.Stat(composeFile); err != nil {
		return fmt.Errorf("compose: Up: compose file %s: %w", composeFile, err)
	}

	project := req.Extras["compose.project"]
	if project == "" {
		project = composeProjectName(worktreeName, file)
	}
	envPrefix := req.Extras["compose.env_prefix"]
	if envPrefix == "" {
		envPrefix = strings.ToUpper(service)
	}

	// container_name is deterministic by host port alone (see
	// renderOverride's doc), so a container already running under this
	// exact name IS this same worktree's own service — reuse it rather
	// than treating its own published port as a foreign conflict.
	// UpOrReuse also removes a stale STOPPED container of this name
	// (e.g. left over from an interrupted prior run) before signalling
	// "not reusable," so the create path below never hits a stale
	// name collision either.
	containerName := fmt.Sprintf("bough-compose-%d", port)
	cli, err := dockerutil.NewClient()
	if err != nil {
		return fmt.Errorf("compose: Up: docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	reuse, err := dockerutil.UpOrReuse(ctx, cli, containerName)
	if err != nil {
		return fmt.Errorf("compose: Up: reuse check %s: %w", containerName, err)
	}

	overridePath, err := writeOverrideFile(req.WorktreeRoot, port, overrideSpec{
		Service: service, TargetPort: targetPort, HostPort: port,
	})
	if err != nil {
		return fmt.Errorf("compose: Up: %w", err)
	}

	if !reuse {
		// Only a GENUINE fresh start reaches here — checking port
		// availability now (rather than unconditionally, which would
		// wrongly flag this worktree's own already-running container
		// as a foreign conflict on every reuse) still closes the gap
		// docker compose up's own bind failure does not reliably
		// surface on every backend (Docker Desktop's macOS proxy layer
		// can silently paper over a host-side conflict a native Linux
		// daemon would reject) — same pattern the mysql/postgres/
		// redis/elasticsearch docker.go plugins already use via this
		// exact helper.
		if !dockerutil.IsPortFree(port) {
			return fmt.Errorf("compose: Up: port %d already in use on 127.0.0.1 — stop the conflicting service or move bough's port range", port)
		}

		upCmd := exec.CommandContext(ctx, "docker", "compose",
			"-f", composeFile, "-f", overridePath, "-p", project, "up", "-d", service)
		if out, err := upCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("compose: Up: docker compose up failed: %w\n%s", err, out)
		}

		// Verify the override's port mapping actually took effect
		// rather than trusting it silently — CONTRACT.md's own
		// reachability clause expects this class of check for every
		// backend.
		portCmd := exec.CommandContext(ctx, "docker", "compose",
			"-f", composeFile, "-f", overridePath, "-p", project, "port", service, strconv.Itoa(targetPort))
		out, err := portCmd.Output()
		if err != nil {
			return fmt.Errorf("compose: Up: docker compose port: %w", err)
		}
		if bound := parseBoundPort(string(out)); bound != port {
			return fmt.Errorf("compose: Up: container published port %d, want %d (override did not take effect)", bound, port)
		}
	}

	// Persisted/cached regardless of reuse-vs-fresh: Down/Cleanup/
	// EnvVars/ReadyCheck must work correctly even when THIS process
	// instance is not the one that originally created the container
	// (pluginhost spawns one fresh subprocess per bough create call).
	st := &upState{
		File: file, Service: service, Project: project,
		TargetPort: targetPort, HostPort: port, EnvPrefix: envPrefix,
		ReadyProbe: req.Extras["compose.ready_probe"],
	}
	if err := writeSidecarState(req.WorktreeRoot, port, st); err != nil {
		return fmt.Errorf("compose: Up: write sidecar state: %w", err)
	}
	p.cacheState(port, st)
	return nil
}

// writeOverrideFile renders and persists the override fragment under
// the engine-provider repo's own .local/ scratch dir, matching the
// convention the other four plugins already use there (flake dirs,
// startup logs).
func writeOverrideFile(worktreeRoot string, port int, spec overrideSpec) (string, error) {
	data, err := renderOverride(spec)
	if err != nil {
		return "", fmt.Errorf("render override: %w", err)
	}
	dir := filepath.Join(worktreeRoot, ".local")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, fmt.Sprintf("bough-compose-%d-override.yml", port))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write override: %w", err)
	}
	return path, nil
}

// parseBoundPort extracts the port number from a `docker compose
// port <service> <containerPort>` output line (typically
// "0.0.0.0:56123" or "127.0.0.1:56123"). Returns 0 if the line cannot
// be parsed, letting the caller treat that as a verification failure
// rather than panicking on malformed CLI output.
func parseBoundPort(out string) int {
	line := strings.TrimSpace(out)
	idx := strings.LastIndex(line, ":")
	if idx < 0 || idx == len(line)-1 {
		return 0
	}
	port, err := strconv.Atoi(line[idx+1:])
	if err != nil {
		return 0
	}
	return port
}

// Down stops only the target service (never `down`, which would also
// remove the shared project network/volumes and any sibling services
// the same compose file declares). It re-derives the override from
// sidecar state rather than requiring it still exist on disk, since
// DownReq carries no Extras to reconstruct it from directly.
func (p *Provider) Down(ctx context.Context, req *api.DownReq) error {
	port := firstListenPort(req.Ports)
	if port <= 0 {
		return fmt.Errorf("compose: Down: invalid ports %v", req.Ports)
	}
	if req.WorktreeRoot == "" {
		return errors.New("compose: Down: WorktreeRoot is required")
	}

	st, err := readSidecarState(req.WorktreeRoot, port)
	if err != nil {
		if os.IsNotExist(err) {
			// Up never completed for this port (e.g. it failed before
			// reaching the sidecar write) — Down must still be
			// idempotent rather than erroring on "nothing to do."
			return nil
		}
		return fmt.Errorf("compose: Down: read sidecar state: %w", err)
	}

	composeFile := api.ResolveUnderRawWorktreeRoot(req.WorktreeRoot, st.File)
	overridePath, err := writeOverrideFile(req.WorktreeRoot, port, overrideSpec{
		Service: st.Service, TargetPort: st.TargetPort, HostPort: st.HostPort,
	})
	if err != nil {
		return fmt.Errorf("compose: Down: %w", err)
	}

	timeoutSec := req.GracefulTimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 10
	}
	gctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	stopCmd := exec.CommandContext(gctx, "docker", "compose",
		"-f", composeFile, "-f", overridePath, "-p", st.Project, "stop", st.Service)
	if out, err := stopCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("compose: Down: docker compose stop failed: %w\n%s", err, out)
	}

	// container_name is deterministic by HOST PORT ALONE (bough-
	// compose-<port>), not scoped by project — stop-without-remove
	// would leave that name occupied by an Exited container, so a
	// LATER worktree that gets allocated the same (now-free) port
	// hits a container-name conflict on its own Up() instead of
	// cleanly reusing the name. `rm` (no -v) removes only the
	// container instance, never the compose-managed volumes Cleanup
	// intentionally leaves alone.
	rmCmd := exec.CommandContext(ctx, "docker", "compose",
		"-f", composeFile, "-f", overridePath, "-p", st.Project, "rm", "-f", st.Service)
	if out, err := rmCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("compose: Down: docker compose rm failed: %w\n%s", err, out)
	}
	return nil
}

// Cleanup is an intentional no-op — see the package doc for why
// (compose owns its own named volumes; this signature has no channel
// to address them, and reaching into an operator-owned compose
// project's storage is arguably not this plugin's business anyway).
func (p *Provider) Cleanup(_ context.Context, _ string, _ []int) error {
	return nil
}

// EnvVars synthesizes BOUGH_<prefix>_HOST/_PORT(/_URL) from the
// in-process cache Up() populated. With no cached state (e.g.
// EnvVars called without a preceding Up() in this process — should
// not happen in production, but defensive) it falls back to a generic
// "COMPOSE" prefix and omits the URL, since the service name itself
// is unknown.
func (p *Provider) EnvVars(_ context.Context, req *api.EnvVarsReq) (map[string]string, error) {
	port := api.PickMainPort(req.Ports)
	prefix := "COMPOSE"
	service := ""
	if st := p.cachedState(port); st != nil {
		if st.EnvPrefix != "" {
			prefix = st.EnvPrefix
		}
		service = st.Service
	}
	out := map[string]string{
		"BOUGH_" + prefix + "_HOST": "127.0.0.1",
		"BOUGH_" + prefix + "_PORT": strconv.Itoa(port),
	}
	if scheme, ok := composeURLSchemes[strings.ToLower(service)]; ok {
		out["BOUGH_"+prefix+"_URL"] = fmt.Sprintf("%s://127.0.0.1:%d", scheme, port)
	}
	return out, nil
}
