//go:build darwin || linux

// Docker backend for the Redis plugin. Same shape as the mysql /
// postgres backends; redis is the simplest of the four because the
// official image is env-less, the cold start is ~1-2s and the
// readiness signal is unambiguous (`redis-cli ping` → `PONG`).
//
// Patterns lifted from:
//
//   - testcontainers-go modules/redis (`wait.ForListeningPort` +
//     `wait.ForLog("* Ready to accept connections")`, both verbatim)
//   - Docker Hub `redis` official image
//     (`--save 60 1 --loglevel warning --appendonly yes` is the
//     documented persistence-enabled invocation; 127.0.0.1 binding
//     is mandated by the image's own security note)
//
// Engine-specific choices:
//
//   - Default image is `redis:7-alpine` (≈5 MB) — Docker Hub recommends
//     alpine for footprint reasons and the bough use-case does not
//     need glibc-only Redis modules.
//   - `--appendonly yes` flips AOF on so a SIGKILL on the container
//     loses ≤1 s of writes instead of the full save-window (60s).
//   - `--bind 0.0.0.0` is intentional — the redis-server defaults to
//     bind 127.0.0.1 which is the container's loopback, unreachable
//     from the host. We rely on Docker's `-p 127.0.0.1:<host>:6379`
//     publish to keep external access shut.
//
// Generic Docker plumbing lives in pkg/dockerutil; only the redis-
// specific concerns (redis-cli ping, persistence flags, optional
// requirepass) stay here.
package redis

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"

	"github.com/ikeikeikeike/bough/pkg/dockerutil"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

const (
	dockerEngine         = "redis"
	dockerDefaultImage   = "redis:7-alpine"
	dockerInternalPort   = "6379/tcp"
	dockerDataDir        = "/data"
	dockerStopTimeoutSec = 5
	dockerReadyPollMS    = 300
)

func pickDockerImage(req *api.UpReq) string {
	if v := req.Extras["docker.image"]; v != "" {
		return v
	}
	if v := req.Extras["version"]; v != "" {
		return fmt.Sprintf("redis:%s-alpine", v)
	}
	return dockerDefaultImage
}

func dockerContainerName(port int) string {
	return fmt.Sprintf("bough-redis-%d", port)
}

func usingDockerBackend(ctx context.Context, port int) bool {
	if port <= 0 {
		return false
	}
	cli, err := dockerutil.NewClient()
	if err != nil {
		return false
	}
	defer func() { _ = cli.Close() }()
	id, err := dockerutil.LookupByName(ctx, cli, dockerContainerName(port))
	if err != nil || id == "" {
		return false
	}
	// A stopped/leftover container must not count as "docker backend
	// in use" — LookupByName lists with All:true, so a stale, already-
	// stopped container from a prior run would otherwise make Down()
	// take the docker path (stop+remove the irrelevant container,
	// report success) while the real engine for this worktree/port —
	// possibly nix-backed — keeps running untouched, and the
	// subsequent Cleanup() would then rm -rf its datadir out from
	// under it.
	info, err := cli.ContainerInspect(ctx, id)
	if err != nil || info.State == nil {
		return false
	}
	return info.State.Running
}

func (p *Provider) dockerUp(ctx context.Context, req *api.UpReq) error {
	port := api.PickMainPort(req.Ports)
	if port <= 0 {
		return fmt.Errorf("redis docker: invalid port %d (Ports=%v)", port, req.Ports)
	}
	if req.Datadir == "" {
		return errors.New("redis docker: datadir is required")
	}

	cli, err := dockerutil.NewClient()
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	imageRef := pickDockerImage(req)
	name := dockerContainerName(port)

	skip, err := dockerutil.UpOrReuse(ctx, cli, name)
	if err != nil {
		return fmt.Errorf("redis docker: reuse check %s: %w", name, err)
	}
	if skip {
		return nil
	}

	if !dockerutil.IsPortFree(port) {
		return fmt.Errorf("redis docker: port %d already in use on 127.0.0.1 — stop the conflicting service or move bough's port range", port)
	}

	if err := dockerutil.PullIfMissing(ctx, cli, imageRef); err != nil {
		return fmt.Errorf("redis docker: pull %s: %w", imageRef, err)
	}

	hostPort := fmt.Sprintf("%d", port)
	portBindings := nat.PortMap{
		nat.Port(dockerInternalPort): []nat.PortBinding{
			{HostIP: "127.0.0.1", HostPort: hostPort},
		},
	}
	exposed := nat.PortSet{
		nat.Port(dockerInternalPort): struct{}{},
	}

	// `--bind 0.0.0.0` lets redis accept connections forwarded from the
	// container's loopback proxy (= host 127.0.0.1:<host-port> via
	// Docker port publish). The publish target above is 127.0.0.1, so
	// the redis socket is still not externally reachable.
	cmd := []string{
		"redis-server",
		"--bind", "0.0.0.0",
		"--save", "60", "1",
		"--loglevel", "warning",
		"--appendonly", "yes",
	}
	if pw := req.Extras["redis.password"]; pw != "" {
		cmd = append(cmd, "--requirepass", pw)
	}

	cfg := &container.Config{
		Image:        imageRef,
		Labels:       dockerutil.Labels(dockerEngine, imageRef, port),
		ExposedPorts: exposed,
		Cmd:          cmd,
	}
	hostCfg := &container.HostConfig{
		Binds:         []string{req.Datadir + ":" + dockerDataDir},
		PortBindings:  portBindings,
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, name)
	if err != nil {
		return fmt.Errorf("redis docker: create: %w", err)
	}
	if err := dockerutil.StartOrCleanup(ctx, cli, resp.ID, "redis", port); err != nil {
		return err
	}
	return nil
}

// dockerReadyCheck polls a TCP dial against the host-side port, then
// runs `redis-cli ping` inside the container to confirm the server has
// finished AOF loading and replies `PONG`.
func (p *Provider) dockerReadyCheck(ctx context.Context, port, timeoutSec int) (bool, error) {
	if timeoutSec <= 0 {
		timeoutSec = 600
	}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)

	cli, err := dockerutil.NewClient()
	if err != nil {
		return false, err
	}
	defer func() { _ = cli.Close() }()
	name := dockerContainerName(port)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}
		conn, dialErr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if dialErr != nil {
			time.Sleep(dockerReadyPollMS * time.Millisecond)
			continue
		}
		_ = conn.Close()
		if redisPing(ctx, cli, name) == nil {
			return true, nil
		}
		time.Sleep(dockerReadyPollMS * time.Millisecond)
	}
	return false, fmt.Errorf("redis docker: not ready on port %d within %ds", port, timeoutSec)
}

// redisPing runs `redis-cli ping` inside the container. Exit 0 +
// stdout `PONG` means the server has finished loading any persistence
// snapshot and is accepting commands.
func redisPing(ctx context.Context, cli *client.Client, name string) error {
	id, err := dockerutil.LookupByName(ctx, cli, name)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("container %s not found", name)
	}
	exec, err := cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          []string{"redis-cli", "ping"},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return err
	}
	if err := cli.ContainerExecStart(ctx, exec.ID, container.ExecStartOptions{}); err != nil {
		return err
	}
	insp, err := cli.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return err
	}
	if insp.Running {
		time.Sleep(150 * time.Millisecond)
		insp, _ = cli.ContainerExecInspect(ctx, exec.ID)
	}
	if insp.ExitCode != 0 {
		return fmt.Errorf("redis-cli ping exit %d", insp.ExitCode)
	}
	return nil
}

func (p *Provider) dockerDown(ctx context.Context, req *api.DownReq) error {
	port := firstListenPort(req.Ports)
	cli, err := dockerutil.NewClient()
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	name := dockerContainerName(port)
	id, err := dockerutil.LookupByName(ctx, cli, name)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	timeout := dockerStopTimeoutSec
	if req.GracefulTimeoutSec > 0 {
		timeout = req.GracefulTimeoutSec
	}
	_ = cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout})
	return cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: false})
}
