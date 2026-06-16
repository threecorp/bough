//go:build darwin || linux

// Docker backend for the Elasticsearch plugin. Most involved of the
// four: ES needs memlock + nofile ulimits, vm.max_map_count >= 262144
// on the host (= Docker Desktop VM on macOS, the user's kernel on
// Linux), and a JVM warmup budget the others do not have.
//
// Patterns lifted from:
//
//   - Elastic 7.17 Docker reference (ulimits, vm.max_map_count, the
//     `discovery.type=single-node` env knob, UID 1000:0 ownership
//     requirement)
//   - testcontainers-go modules/elasticsearch (`wait.ForHTTP("/")`
//     against the published 9200 port as the canonical ready signal,
//     plus the `cluster.routing.allocation.disk.threshold_enabled=
//     false` env to silence the dev-mode disk warning that otherwise
//     turns the cluster red)
//
// Engine-specific choices:
//
//   * Default image is `docker.elastic.co/elasticsearch/elasticsearch:
//     7.17.29` — the last 7-line LTS patch with first-class linux/arm64
//     support. Override via `extras["docker.image"]`.
//   * `ES_JAVA_OPTS=-Xms1g -Xmx1g` deliberately undersized for laptops
//     running 5-15 parallel worktrees. Override via
//     `extras["es.heap"]="2g"` if a single-worktree workflow can afford
//     more.
//   * HTTP readiness against `/` (not `_cluster/health?wait_for_status=
//     yellow`) because the root endpoint returns 200 once the cluster
//     is yellow-or-better — single-node ES is always yellow because
//     there is no replica to assign — and the request is cheaper.
//   * Stop timeout is 60s — ES translog flush + Lucene commit can run
//     long on a populated index.
//
// Generic Docker plumbing lives in pkg/dockerutil; ES-specific concerns
// (ulimits, datadir chown, JVM heap, HTTP readiness) stay here.
package elasticsearch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/db/api"

	"github.com/ikeikeikeike/bough/pkg/dockerutil"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/docker/go-units"
)

const (
	dockerEngine         = "elasticsearch"
	dockerDefaultImage   = "docker.elastic.co/elasticsearch/elasticsearch:7.17.29"
	dockerInternalHTTP   = "9200/tcp"
	dockerInternalTrans  = "9300/tcp"
	dockerDataDir        = "/usr/share/elasticsearch/data"
	dockerStopTimeoutSec = 60
	dockerReadyPollMS    = 1000
)

func pickDockerImage(req api.UpReq) string {
	if v := req.Extras["docker.image"]; v != "" {
		return v
	}
	if v := req.Extras["version"]; v != "" {
		return "docker.elastic.co/elasticsearch/elasticsearch:" + v
	}
	return dockerDefaultImage
}

func pickHeap(req api.UpReq) string {
	if v := req.Extras["es.heap"]; v != "" {
		return v
	}
	return "1g"
}

func dockerContainerName(port int) string {
	return fmt.Sprintf("bough-elasticsearch-%d", port)
}

func usingDockerBackend(ctx context.Context, port int) bool {
	if port <= 0 {
		return false
	}
	cli, err := dockerutil.NewClient()
	if err != nil {
		return false
	}
	defer cli.Close()
	id, err := dockerutil.LookupByName(ctx, cli, dockerContainerName(port))
	if err != nil {
		return false
	}
	return id != ""
}

func (p *Provider) dockerUp(ctx context.Context, req api.UpReq) error {
	if req.Port <= 0 {
		return fmt.Errorf("elasticsearch docker: invalid port %d", req.Port)
	}
	if req.Datadir == "" {
		return errors.New("elasticsearch docker: datadir is required")
	}

	cli, err := dockerutil.NewClient()
	if err != nil {
		return err
	}
	defer cli.Close()

	imageRef := pickDockerImage(req)
	name := dockerContainerName(req.Port)

	skip, err := dockerutil.UpOrReuse(ctx, cli, name)
	if err != nil {
		return fmt.Errorf("elasticsearch docker: reuse check %s: %w", name, err)
	}
	if skip {
		return nil
	}

	if !dockerutil.IsPortFree(req.Port) {
		return fmt.Errorf("elasticsearch docker: port %d already in use on 127.0.0.1 — stop the conflicting service or move bough's port range", req.Port)
	}

	if err := dockerutil.PullIfMissing(ctx, cli, imageRef); err != nil {
		return fmt.Errorf("elasticsearch docker: pull %s: %w", imageRef, err)
	}

	// Pre-create + chown the bind-mounted datadir to UID:GID 1000:0 so
	// the elasticsearch process (which runs as uid 1000 in the official
	// image) can write to it. Without this the container exits with
	// `AccessDeniedException` on its first index.create.
	if err := os.MkdirAll(req.Datadir, 0o755); err != nil {
		return fmt.Errorf("elasticsearch docker: mkdir datadir: %w", err)
	}
	if err := chownDatadirIfPossible(req.Datadir); err != nil {
		// Soft-fail: macOS Docker Desktop hides the host uid via the
		// VirtioFS proxy, so chown is unnecessary there. Linux users
		// without root cannot chown to 1000:0 — they need to either
		// run bough with sudo or set the datadir owner manually.
		// Log the soft-fail via a stderr-style annotation but proceed.
		_ = err
	}

	heap := pickHeap(req)
	env := []string{
		"discovery.type=single-node",
		"xpack.security.enabled=false",
		"cluster.routing.allocation.disk.threshold_enabled=false",
		"ES_JAVA_OPTS=-Xms" + heap + " -Xmx" + heap,
	}

	hostPort := fmt.Sprintf("%d", req.Port)
	portBindings := nat.PortMap{
		nat.Port(dockerInternalHTTP): []nat.PortBinding{
			{HostIP: "127.0.0.1", HostPort: hostPort},
		},
		// Transport port stays internal — sibling worktrees never gossip,
		// and exposing 9300 collides across multiple ES instances on the
		// same host.
	}
	exposed := nat.PortSet{
		nat.Port(dockerInternalHTTP):  struct{}{},
		nat.Port(dockerInternalTrans): struct{}{},
	}

	cfg := &container.Config{
		Image:        imageRef,
		Env:          env,
		Labels:       dockerutil.Labels(dockerEngine, imageRef, req.Port),
		ExposedPorts: exposed,
	}
	hostCfg := &container.HostConfig{
		Binds:        []string{req.Datadir + ":" + dockerDataDir},
		PortBindings: portBindings,
		Resources: container.Resources{
			Ulimits: []*units.Ulimit{
				// memlock unlimited so bootstrap.memory_lock can succeed.
				{Name: "memlock", Hard: -1, Soft: -1},
				// nofile per Elastic 7.17 docs.
				{Name: "nofile", Hard: 65535, Soft: 65535},
			},
		},
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, name)
	if err != nil {
		return fmt.Errorf("elasticsearch docker: create: %w", err)
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true, RemoveVolumes: false})
		if strings.Contains(err.Error(), "port is already allocated") {
			return fmt.Errorf("elasticsearch docker: host port %d is already published by another container — `docker ps --filter publish=%d` to find it; raw: %w", req.Port, req.Port, err)
		}
		return fmt.Errorf("elasticsearch docker: start %s: %w", resp.ID, err)
	}
	return nil
}

// chownDatadirIfPossible recursively chowns the bind-mount target to
// elasticsearch's container UID:GID (1000:0). On macOS / Windows
// Docker Desktop the bind-mount layer maps host ownership through a
// VirtioFS proxy so this is a no-op; on Linux it's necessary when
// bough runs as root (CI). Returns an error if chown fails — the
// caller treats it as soft-fail.
func chownDatadirIfPossible(datadir string) error {
	return os.Chown(datadir, 1000, 0)
}

// dockerReadyCheck polls TCP listen on the host-side HTTP port, then
// issues an HTTP GET against http://127.0.0.1:<port>/ until 200. ES
// returns 200 on `/` once the cluster is yellow-or-better — single-
// node ES is always yellow because there is no replica to assign, so
// this is the canonical "ready for queries" signal.
func (p *Provider) dockerReadyCheck(ctx context.Context, port, timeoutSec int) (bool, error) {
	if timeoutSec <= 0 {
		timeoutSec = 600
	}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	url := fmt.Sprintf("http://%s/", addr)
	httpClient := &http.Client{Timeout: 2 * time.Second}

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
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true, nil
			}
		}
		time.Sleep(dockerReadyPollMS * time.Millisecond)
	}
	return false, fmt.Errorf("elasticsearch docker: not ready on port %d within %ds", port, timeoutSec)
}

func (p *Provider) dockerDown(ctx context.Context, req api.DownReq) error {
	cli, err := dockerutil.NewClient()
	if err != nil {
		return err
	}
	defer cli.Close()
	name := dockerContainerName(req.Port)
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

func (p *Provider) dockerCleanup(_ context.Context, datadir string, _ int) error {
	if datadir == "" {
		return errors.New("elasticsearch docker: Cleanup: datadir is required")
	}
	return os.RemoveAll(datadir)
}
