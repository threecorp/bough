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
//   * Idempotent Up: remove any container with the same name before
//     create (mysqld crashes leave a stopped container; v0.1 used to
//     fail-fast on conflict).
//   * IfNotPresent pull: ImageInspect short-circuits the pull when the
//     image is already in the local cache, so warm cold-starts skip the
//     network round-trip.
//   * Graceful Down: 30 s stop timeout matches the InnoDB recovery
//     budget per the MySQL 8.4 server docs.
//   * 127.0.0.1 binding only: never publish to 0.0.0.0; the per-worktree
//     mysqld is dev-only and exposing it would let a sibling worktree
//     hit the same port from outside.
package mysql

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/db/api"

	"github.com/docker/go-connections/nat"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

const (
	dockerDefaultImage   = "mysql:8.4"
	dockerInternalPort   = "3306/tcp"
	dockerInternalMysqlx = "33060/tcp"
	dockerDataDir        = "/var/lib/mysql"
	dockerStopTimeoutSec = 30
	dockerReadyPollMS    = 500
)

// pickDockerImage honours `extras["docker.image"]` and falls back to the
// version-derived tag (`mysql:<version>`), then to dockerDefaultImage.
func pickDockerImage(req api.UpReq) string {
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
	cli, err := newDockerClient()
	if err != nil {
		return false
	}
	defer cli.Close()
	id, err := lookupByName(ctx, cli, dockerContainerName(port))
	if err != nil {
		return false
	}
	return id != ""
}

func dockerLabels(imageRef string, port int) map[string]string {
	return map[string]string{
		"com.bough.managed":   "true",
		"com.bough.engine":    "mysql",
		"com.bough.image":     imageRef,
		"com.bough.host-port": fmt.Sprintf("%d", port),
	}
}

// newDockerClient connects to whichever Docker-compatible daemon the
// caller's DOCKER_HOST points at (Docker Desktop / OrbStack / Colima /
// rootless podman). API negotiation lets the SDK match the daemon's
// version without us pinning here.
func newDockerClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return cli, nil
}

// pullIfMissing implements the if_not_present pull policy. The Docker
// API's ImagePull is a stream we must drain before the layer download
// is reflected on the daemon.
func pullIfMissing(ctx context.Context, cli *client.Client, ref string) error {
	if _, err := cli.ImageInspect(ctx, ref); err == nil {
		return nil
	}
	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("drain pull stream: %w", err)
	}
	return nil
}

// removeIfExists is the idempotency helper for dockerUp — if a previous
// run left a stopped (or running) container with the same name we tear
// it down so ContainerCreate does not collide.
func removeIfExists(ctx context.Context, cli *client.Client, name string) error {
	id, err := lookupByName(ctx, cli, name)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	return cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: false})
}

// upOrReuse implements the --resume idempotency contract for Up.
// Returns skip=true if a container with `name` is already running, so
// the caller can early-return without recreating. If the container
// exists but is stopped (a previous Up partially failed), the stale
// container is removed and skip=false is returned so the caller
// proceeds with a fresh create + start.
//
// Mirrors threecorp scripts/worktree-create.sh:46-50 — "skip if
// worktree already exists" but at the container layer.
func upOrReuse(ctx context.Context, cli *client.Client, name string) (bool, error) {
	id, err := lookupByName(ctx, cli, name)
	if err != nil {
		return false, err
	}
	if id == "" {
		return false, nil
	}
	if info, ierr := cli.ContainerInspect(ctx, id); ierr == nil && info.State != nil && info.State.Running {
		return true, nil
	}
	if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
		return false, err
	}
	return false, nil
}

// isPortFree probes whether the host's loopback port is currently free
// by attempting a short-lived `net.Listen`. A taken port short-circuits
// dockerUp before the daemon's generic "port is already allocated"
// error, so the operator sees an actionable message instead of having
// to grep `docker ps` for the conflict.
func isPortFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

func lookupByName(ctx context.Context, cli *client.Client, name string) (string, error) {
	args := filters.NewArgs()
	args.Add("name", "^/"+name+"$")
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return "", err
	}
	for _, c := range list {
		for _, n := range c.Names {
			if strings.TrimPrefix(n, "/") == name {
				return c.ID, nil
			}
		}
	}
	return "", nil
}

func (p *Provider) dockerUp(ctx context.Context, req api.UpReq) error {
	if req.Port <= 0 {
		return fmt.Errorf("mysql docker: invalid port %d", req.Port)
	}
	if req.Datadir == "" {
		return errors.New("mysql docker: datadir is required")
	}

	cli, err := newDockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()

	imageRef := pickDockerImage(req)
	name := dockerContainerName(req.Port)

	// Idempotency: claude --resume re-fires WorktreeCreate, so an
	// already-running container is a successful no-op.
	skip, err := upOrReuse(ctx, cli, name)
	if err != nil {
		return fmt.Errorf("mysql docker: reuse check %s: %w", name, err)
	}
	if skip {
		return nil
	}

	// Pre-flight: actionable error instead of the daemon's generic
	// "port is already allocated" when the host port is taken (e.g.
	// by a probe-mysql on the same port from a separate dev session).
	if !isPortFree(req.Port) {
		return fmt.Errorf("mysql docker: port %d already in use on 127.0.0.1 — stop the conflicting service or move bough's port range", req.Port)
	}

	if err := pullIfMissing(ctx, cli, imageRef); err != nil {
		return fmt.Errorf("mysql docker: pull %s: %w", imageRef, err)
	}

	initDB := "bough"
	if len(req.InitialDatabases) > 0 {
		initDB = req.InitialDatabases[0]
	}
	env := []string{
		"MYSQL_ALLOW_EMPTY_PASSWORD=yes",
		"MYSQL_DATABASE=" + initDB,
	}
	// Per-engine extras lift verbatim from YAML `databases[].extras` so
	// projects can pin character_set_server, default_time_zone, etc.
	// without a plugin change.
	for k, v := range req.Extras {
		if strings.HasPrefix(k, "docker.") || k == "version" || k == "backend" {
			continue
		}
		env = append(env, "MYSQL_"+strings.ToUpper(k)+"="+v)
	}

	hostPort := fmt.Sprintf("%d", req.Port)
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
		Labels:       dockerLabels(imageRef, req.Port),
		ExposedPorts: exposed,
	}
	hostCfg := &container.HostConfig{
		Binds:        []string{req.Datadir + ":" + dockerDataDir},
		PortBindings: portBindings,
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
			return fmt.Errorf("mysql docker: host port %d is already published by another container — `docker ps --filter publish=%d` to find it; raw: %w", req.Port, req.Port, err)
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

	cli, err := newDockerClient()
	if err != nil {
		return false, err
	}
	defer cli.Close()
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
	id, err := lookupByName(ctx, cli, name)
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

func (p *Provider) dockerDown(ctx context.Context, req api.DownReq) error {
	cli, err := newDockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()
	name := dockerContainerName(req.Port)
	id, err := lookupByName(ctx, cli, name)
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
func (p *Provider) dockerCleanup(_ context.Context, datadir string, _ int) error {
	if datadir == "" {
		return errors.New("mysql docker: Cleanup: datadir is required")
	}
	return os.RemoveAll(datadir)
}
