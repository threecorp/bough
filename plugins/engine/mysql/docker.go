//go:build darwin || linux

// Docker backend for the MySQL plugin. Lifecycle ops (Up / ReadyCheck /
// Down / Cleanup) bind-mount the worktree datadir into a long-lived
// `mysql:8.4` container, port-publish 3306 to `127.0.0.1:<bough port>`
// and label the container with `com.bough.*` so cleanup paths can
// rediscover it without an external registry.
//
// Patterns are lifted from:
//
//   - testcontainers-go modules/mysql       — wait/log/port strategies
//   - ory/dockertest v4                     — moby/moby/client direct usage
//   - Docker Hub `mysql` official image     — ENV scheme + /docker-entrypoint-initdb.d
//   - coast-guard/coasts coast-docker crate — com.bough.* label naming
//
// Stable-operation choices:
//
//   - Idempotent Up: remove any container with the same name before
//     create (mysqld crashes leave a stopped container; v0.1 used to
//     fail-fast on conflict).
//   - IfNotPresent pull: ImageInspect short-circuits the pull when the
//     image is already in the local cache, so warm cold-starts skip the
//     network round-trip.
//   - Graceful Down: 30 s stop timeout matches the InnoDB recovery
//     budget per the MySQL 8.4 server docs.
//   - 127.0.0.1 binding only: never publish to 0.0.0.0; the per-worktree
//     mysqld is dev-only and exposing it would let a sibling worktree
//     hit the same port from outside.
//
// Generic Docker plumbing (client construction, image pull, container
// lookup/remove/reuse, host port probe, com.bough.* label schema) lives
// in pkg/dockerutil; only the mysql-specific concerns (mysqladmin ping,
// MYSQL_* env scheme, port set) stay here.
package mysql

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"

	"github.com/ikeikeikeike/bough/pkg/dockerutil"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

const (
	dockerEngine         = "mysql"
	dockerDefaultImage   = "mysql:8.4"
	dockerInternalPort   = "3306/tcp"
	dockerInternalMysqlx = "33060/tcp"
	dockerDataDir        = "/var/lib/mysql"
	dockerStopTimeoutSec = 30
	dockerReadyPollMS    = 500
)

// pickDockerImage honours `extras["docker.image"]` and falls back to the
// version-derived tag (`mysql:<version>`), then to dockerDefaultImage.
func pickDockerImage(req *api.UpReq) string {
	if v := req.Extras["docker.image"]; v != "" {
		return v
	}
	if v := req.Extras["version"]; v != "" {
		return "mysql:" + v
	}
	return dockerDefaultImage
}

func dockerContainerName(port int) string {
	return fmt.Sprintf("bough-mysql-%d", port)
}

// usingDockerBackend is the cheap self-detection used by Down /
// ReadyCheck when neither RPC carries an explicit backend hint. A
// container named `bough-mysql-<port>` is uniquely owned by this
// plugin (the bough port allocator guarantees worktree-scoped
// uniqueness), so finding one is sufficient evidence that Up went
// via the Docker path.
//
// Returns false on any Docker error so the caller cleanly falls
// through to the nix path — that keeps `bough remove` working on
// machines where Docker was uninstalled between create and remove.
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
	if err != nil {
		return false
	}
	return id != ""
}

func (p *Provider) dockerUp(ctx context.Context, req *api.UpReq) error {
	port := api.PickMainPort(req.Ports)
	if port <= 0 {
		return fmt.Errorf("mysql docker: invalid port %d (Ports=%v)", port, req.Ports)
	}
	if req.Datadir == "" {
		return errors.New("mysql docker: datadir is required")
	}

	cli, err := dockerutil.NewClient()
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	imageRef := pickDockerImage(req)
	name := dockerContainerName(port)

	// Idempotency: claude --resume re-fires WorktreeCreate, so an
	// already-running container is a successful no-op.
	skip, err := dockerutil.UpOrReuse(ctx, cli, name)
	if err != nil {
		return fmt.Errorf("mysql docker: reuse check %s: %w", name, err)
	}
	if skip {
		return nil
	}

	// Pre-flight: actionable error instead of the daemon's generic
	// "port is already allocated" when the host port is taken (e.g.
	// by a probe-mysql on the same port from a separate dev session).
	if !dockerutil.IsPortFree(port) {
		return fmt.Errorf("mysql docker: port %d already in use on 127.0.0.1 — stop the conflicting service or move bough's port range", port)
	}

	if err := dockerutil.PullIfMissing(ctx, cli, imageRef); err != nil {
		return fmt.Errorf("mysql docker: pull %s: %w", imageRef, err)
	}

	initDB := api.PickFirstResourceName(req.InitialResources, "database")
	if initDB == "" {
		initDB = "bough"
	}
	env := []string{
		"MYSQL_ALLOW_EMPTY_PASSWORD=yes",
		"MYSQL_DATABASE=" + initDB,
	}
	// Per-engine extras lift verbatim from YAML `engines[].extras` so
	// projects can pin character_set_server, default_time_zone, etc.
	// without a plugin change.
	for k, v := range req.Extras {
		if strings.HasPrefix(k, "docker.") || k == "version" || k == "backend" {
			continue
		}
		env = append(env, "MYSQL_"+strings.ToUpper(k)+"="+v)
	}

	hostPort := fmt.Sprintf("%d", port)
	portBindings := nat.PortMap{
		nat.Port(dockerInternalPort): []nat.PortBinding{
			{HostIP: "127.0.0.1", HostPort: hostPort},
		},
	}
	exposed := nat.PortSet{
		nat.Port(dockerInternalPort):   struct{}{},
		nat.Port(dockerInternalMysqlx): struct{}{},
	}

	cfg := &container.Config{
		Image:        imageRef,
		Env:          env,
		Labels:       dockerutil.Labels(dockerEngine, imageRef, port),
		ExposedPorts: exposed,
	}
	hostCfg := &container.HostConfig{
		Binds:         []string{req.Datadir + ":" + dockerDataDir},
		PortBindings:  portBindings,
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, name)
	if err != nil {
		return fmt.Errorf("mysql docker: create: %w", err)
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Garbage-collect the just-created stopped container so a
		// retry is not blocked by a stale name; otherwise the next
		// Up would have to remove it first and the operator sees a
		// confusing "name already in use" downstream.
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true, RemoveVolumes: false})
		// macOS Docker Desktop's vpnkit makes the host port look free
		// to `net.Listen`, so we cannot pre-check it reliably; instead
		// detect the daemon's "port is already allocated" error here
		// and rewrap with an actionable hint.
		if strings.Contains(err.Error(), "port is already allocated") {
			return fmt.Errorf("mysql docker: host port %d is already published by another container — `docker ps --filter publish=%d` to find it; raw: %w", port, port, err)
		}
		return fmt.Errorf("mysql docker: start %s: %w", resp.ID, err)
	}
	return nil
}

// dockerReadyCheck polls a TCP dial against the host-side port until it
// succeeds, then issues a single `mysqladmin ping` via docker exec
// against the container's internal socket to confirm mysqld accepts
// queries (the TCP listener opens slightly before mysqld is query-ready
// on a fresh datadir initdb).
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
		if mysqlAdminPing(ctx, cli, name) == nil {
			return true, nil
		}
		time.Sleep(dockerReadyPollMS * time.Millisecond)
	}
	return false, fmt.Errorf("mysql docker: not ready on port %d within %ds", port, timeoutSec)
}

// mysqlAdminPing runs `mysqladmin ping -h localhost` inside the
// container — uses the internal socket, no host network round-trip.
// Returns nil iff mysqld replies `mysqld is alive` (mysqladmin exits 0).
func mysqlAdminPing(ctx context.Context, cli *client.Client, name string) error {
	id, err := dockerutil.LookupByName(ctx, cli, name)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("container %s not found", name)
	}
	exec, err := cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          []string{"mysqladmin", "ping", "-h", "localhost"},
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
		// Brief wait for exec to settle if the daemon is slow.
		time.Sleep(200 * time.Millisecond)
		insp, _ = cli.ContainerExecInspect(ctx, exec.ID)
	}
	if insp.ExitCode != 0 {
		return fmt.Errorf("mysqladmin ping exit %d", insp.ExitCode)
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
		return nil // already gone — Down is idempotent
	}
	timeout := dockerStopTimeoutSec
	if req.GracefulTimeoutSec > 0 {
		timeout = req.GracefulTimeoutSec
	}
	if err := cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout}); err != nil {
		// Stop can race with the daemon already SIGKILL'ing a hung mysqld;
		// fall through to Remove unconditionally.
		_ = err
	}
	return cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: false})
}

// dockerCleanup mirrors the nix path's Cleanup — the bind-mounted
// datadir is host-managed regardless of which backend wrote into it,
// and Down already removed the container itself.
