//go:build darwin || linux

// Docker backend for the PostgreSQL plugin. Mirrors the mysql plugin's
// shape: bind-mount the worktree datadir into a long-lived
// `postgres:16-alpine` container, publish 5432 on 127.0.0.1:<bough
// port>, label for cleanup discovery.
//
// Patterns lifted from:
//
//   - testcontainers-go modules/postgres BasicWaitStrategies (log
//     occurrence-of-2 + port listen, because postgres restarts itself
//     once during initdb)
//   - Docker Hub `postgres` official image (PGDATA, ENV scheme)
//   - testcontainers-go default cmd `postgres -c fsync=off` for dev
//     speed without compromising durability semantics enough to mask
//     bugs (this is a dev environment, not prod)
//
// Engine-specific choices:
//
//   - Pre-init script + pg_isready over docker exec is preferred over
//     scraping container logs because the wait strategy is then driver-
//     agnostic and works against any postgres-compatible engine
//     (CockroachDB, YugabyteDB) the user might swap the image for via
//     `extras["docker.image"]`.
//   - Stop timeout is 15s — postgres "smart shutdown" (SIGTERM) waits
//     for client disconnect, but bough's per-worktree dev environments
//     never have lingering clients, so 15s is generous.
//
// Generic Docker plumbing lives in pkg/dockerutil; the postgres-specific
// concerns (pg_isready exec, POSTGRES_* env scheme, fsync=off dev flag)
// stay here.
package postgres

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
	dockerEngine         = "postgres"
	dockerDefaultImage   = "postgres:16-alpine"
	dockerInternalPort   = "5432/tcp"
	dockerDataDir        = "/var/lib/postgresql/data"
	dockerStopTimeoutSec = 15
	dockerReadyPollMS    = 500
)

func pickDockerImage(req *api.UpReq) string {
	if v := req.Extras["docker.image"]; v != "" {
		return v
	}
	if v := req.Extras["version"]; v != "" {
		// Default to the alpine variant for v0.2; users who need glibc
		// can override via extras["docker.image"]="postgres:16".
		return fmt.Sprintf("postgres:%s-alpine", v)
	}
	return dockerDefaultImage
}

func dockerContainerName(port int) string {
	return fmt.Sprintf("bough-postgres-%d", port)
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
	if err != nil {
		return false
	}
	return id != ""
}

func pickInitDB(req *api.UpReq) string {
	if name := api.PickFirstResourceName(req.InitialResources, "database"); name != "" {
		return name
	}
	return "bough"
}

func (p *Provider) dockerUp(ctx context.Context, req *api.UpReq) error {
	port := api.PickMainPort(req.Ports)
	if port <= 0 {
		return fmt.Errorf("postgres docker: invalid port %d (Ports=%v)", port, req.Ports)
	}
	if req.Datadir == "" {
		return errors.New("postgres docker: datadir is required")
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
		return fmt.Errorf("postgres docker: reuse check %s: %w", name, err)
	}
	if skip {
		return nil
	}

	if !dockerutil.IsPortFree(port) {
		return fmt.Errorf("postgres docker: port %d already in use on 127.0.0.1 — stop the conflicting service or move bough's port range", port)
	}

	if err := dockerutil.PullIfMissing(ctx, cli, imageRef); err != nil {
		return fmt.Errorf("postgres docker: pull %s: %w", imageRef, err)
	}

	password := "bough"
	user := "bough"
	initDB := pickInitDB(req)
	if v := req.Extras["postgres.password"]; v != "" {
		password = v
	}
	if v := req.Extras["postgres.user"]; v != "" {
		user = v
	}
	env := []string{
		"POSTGRES_PASSWORD=" + password,
		"POSTGRES_USER=" + user,
		"POSTGRES_DB=" + initDB,
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

	cfg := &container.Config{
		Image:        imageRef,
		Env:          env,
		Labels:       dockerutil.Labels(dockerEngine, imageRef, port),
		ExposedPorts: exposed,
		// Dev-mode: fsync=off so cold-start initdb finishes faster.
		// Per-worktree dev environments are throwaway; if a worktree
		// crashes mid-write the user re-runs `make migrate` anyway.
		Cmd: []string{"postgres", "-c", "fsync=off"},
	}
	hostCfg := &container.HostConfig{
		Binds:         []string{req.Datadir + ":" + dockerDataDir},
		PortBindings:  portBindings,
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, name)
	if err != nil {
		return fmt.Errorf("postgres docker: create: %w", err)
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true, RemoveVolumes: false})
		if strings.Contains(err.Error(), "port is already allocated") {
			return fmt.Errorf("postgres docker: host port %d is already published by another container — `docker ps --filter publish=%d` to find it; raw: %w", port, port, err)
		}
		return fmt.Errorf("postgres docker: start %s: %w", resp.ID, err)
	}
	return nil
}

// dockerReadyCheck polls a TCP dial against the host-side port until it
// succeeds, then runs `pg_isready` inside the container against the
// internal socket to confirm postgres has finished initdb + the
// automatic restart and is accepting query connections.
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
		if pgIsReady(ctx, cli, name) == nil {
			return true, nil
		}
		time.Sleep(dockerReadyPollMS * time.Millisecond)
	}
	return false, fmt.Errorf("postgres docker: not ready on port %d within %ds", port, timeoutSec)
}

// pgIsReady runs `pg_isready -h localhost` inside the container. Exit
// 0 = accepting, 1 = rejecting (still starting), 2 = no response,
// 3 = no attempt.
func pgIsReady(ctx context.Context, cli *client.Client, name string) error {
	id, err := dockerutil.LookupByName(ctx, cli, name)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("container %s not found", name)
	}
	exec, err := cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          []string{"pg_isready", "-h", "localhost", "-U", "bough"},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return err
	}
	if err := cli.ContainerExecStart(ctx, exec.ID, container.ExecStartOptions{}); err != nil {
		return err
	}
	// pg_isready returns very fast; the first inspect right after start
	// usually shows ExitCode populated. If it does not, give the daemon
	// up to 500 ms to catch up.
	insp, err := cli.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return err
	}
	if insp.Running {
		time.Sleep(200 * time.Millisecond)
		insp, _ = cli.ContainerExecInspect(ctx, exec.ID)
	}
	if insp.ExitCode != 0 {
		return fmt.Errorf("pg_isready exit %d", insp.ExitCode)
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
