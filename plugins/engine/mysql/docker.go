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
//   - Resume idempotency: a running container with the same name is
//     reused as-is (dockerutil.UpOrReuse); a stopped one (mysqld
//     crashed, or a prior Up partially failed) is removed so create
//     doesn't collide.
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
	"regexp"
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
	dockerInternalPort   = "3306/tcp"
	dockerInternalMysqlx = "33060/tcp"
	dockerDataDir        = "/var/lib/mysql"
	dockerStopTimeoutSec = 30
	dockerReadyPollMS    = 500
)

// dockerImage turns `extras["version"]` into the image to run, honouring
// `extras["docker.image"]` verbatim first. Docker Hub publishes short and
// full tags alike (8.4, 9, 8.4.5); a variant such as 8.4-oracle needs
// the docker.image escape hatch.
var dockerImage = api.DockerImage{
	Image:      "mysql:%s",
	Default:    "8.4",
	TagPattern: regexp.MustCompile(`^\d+(\.\d+){0,2}$`),
	TagHint:    "a version tag such as 8.4 or 9",
}

func dockerContainerName(port int) string {
	return fmt.Sprintf("bough-mysql-%d", port)
}

// buildDockerEnv assembles the container Env slice. "database" and
// "allow_empty_password" are reserved: both already have a hardcoded
// MYSQL_* entry below, so an extras key of either name is dropped
// rather than appended as a second, conflicting entry for the same
// env var (the official mysql image's entrypoint script's behavior on
// a duplicate env name is undefined by this code either way).
func buildDockerEnv(initDB string, extras map[string]string) []string {
	env := []string{
		"MYSQL_ALLOW_EMPTY_PASSWORD=yes",
		"MYSQL_DATABASE=" + initDB,
	}
	// Per-engine extras lift verbatim from YAML `engines[].extras` so
	// projects can pin character_set_server, default_time_zone, etc.
	// without a plugin change.
	for k, v := range extras {
		switch {
		case strings.HasPrefix(k, "docker."),
			k == "version", k == "backend",
			k == "database", k == "allow_empty_password":
			continue
		}
		env = append(env, "MYSQL_"+strings.ToUpper(k)+"="+v)
	}
	return env
}

// dockerBackend runs mysqld in a container. Stateless: the tunables
// are the consts above and the image comes from dockerImage.
type dockerBackend struct{}

var _ api.Backend = dockerBackend{}

// Running answers ForPort's disambiguation question. A container named
// `bough-mysql-<port>` is uniquely owned by this plugin (the bough port
// allocator guarantees worktree-scoped uniqueness). See
// dockerutil.IsBackendRunning for the stale-container rule all four
// engine plugins share.
func (dockerBackend) Running(ctx context.Context, port int) bool {
	if port <= 0 {
		return false
	}
	cli, err := dockerutil.NewClient()
	if err != nil {
		return false
	}
	defer func() { _ = cli.Close() }()
	return dockerutil.IsBackendRunning(ctx, cli, dockerContainerName(port))
}

func (dockerBackend) Up(ctx context.Context, req *api.UpReq) error {
	port := api.PickMainPort(req.Ports)
	if port <= 0 {
		return fmt.Errorf("mysql docker: invalid port %d (Ports=%v)", port, req.Ports)
	}
	if req.Datadir == "" {
		return errors.New("mysql docker: datadir is required")
	}

	imageRef, err := dockerImage.Resolve(req.Extras)
	if err != nil {
		return fmt.Errorf("mysql docker: %w", err)
	}

	cli, err := dockerutil.NewClient()
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

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
	env := buildDockerEnv(initDB, req.Extras)

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
	return dockerutil.StartOrCleanup(ctx, cli, resp.ID, "mysql", port)
}

// ReadyCheck polls a TCP dial against the host-side port as a cheap
// pre-gate, then confirms readiness with an in-container SELECT 1
// forced over TCP (mysqlTCPReady). The host-side dial alone is not
// sufficient: docker-proxy accepts the host port from the moment the
// container starts, regardless of whether mysqld is listening yet.
// mysqlTCPReady is what actually distinguishes "the real server is
// serving queries" from "docker-proxy answered".
func (dockerBackend) ReadyCheck(ctx context.Context, port, timeoutSec int) (bool, error) {
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
		if mysqlTCPReady(ctx, cli, name) == nil {
			return true, nil
		}
		time.Sleep(dockerReadyPollMS * time.Millisecond)
	}
	return false, fmt.Errorf("mysql docker: not ready on port %d within %ds", port, timeoutSec)
}

// mysqlTCPReady runs `mysql --protocol=TCP -h127.0.0.1 -uroot -e
// 'SELECT 1'` inside the container via docker exec. --protocol=TCP
// forces the network path (rather than the client's default socket
// preference), so this fails cleanly during the mysql:8.4 image's
// first-run "temporary server" phase, which runs mysqld with
// --skip-networking to bootstrap the datadir/grant tables before
// restarting as the real, network-enabled server. A socket-based
// check (the previous mysqladmin ping -h localhost) answers success
// against that temporary server too, which is a container-internal
// socket connection — it says nothing about whether the real server
// is up, and the real server is what post_create hooks connect to
// over host TCP immediately after Up returns.
func mysqlTCPReady(ctx context.Context, cli *client.Client, name string) error {
	id, err := dockerutil.LookupByName(ctx, cli, name)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("container %s not found", name)
	}
	exec, err := cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          []string{"mysql", "--protocol=TCP", "-h127.0.0.1", "-uroot", "-e", "SELECT 1"},
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
		return fmt.Errorf("mysql --protocol=TCP SELECT 1 exit %d", insp.ExitCode)
	}
	return nil
}

func (dockerBackend) Down(ctx context.Context, req *api.DownReq) error {
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
