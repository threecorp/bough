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
//   - Default image is `docker.elastic.co/elasticsearch/elasticsearch:
//     7.17.29` — the last 7-line LTS patch with first-class linux/arm64
//     support. Override via `extras["docker.image"]`.
//   - `ES_JAVA_OPTS=-Xms1g -Xmx1g` deliberately undersized for laptops
//     running 5-15 parallel worktrees. Override via
//     `extras["es.heap"]="2g"` if a single-worktree workflow can afford
//     more.
//   - HTTP readiness against `/` (not `_cluster/health?wait_for_status=
//     yellow`) because the root endpoint returns 200 once the cluster
//     is yellow-or-better — single-node ES is always yellow because
//     there is no replica to assign — and the request is cheaper.
//   - Stop timeout is 60s — ES translog flush + Lucene commit can run
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
	"time"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"

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

func pickDockerImage(req *api.UpReq) string {
	if v := req.Extras["docker.image"]; v != "" {
		return v
	}
	if v := req.Extras["version"]; v != "" {
		return "docker.elastic.co/elasticsearch/elasticsearch:" + v
	}
	return dockerDefaultImage
}

func pickHeap(req *api.UpReq) string {
	// "es.heap" is this file's own documented key; "heap" is what the
	// nix backend (elasticsearch.go's Up()) has always read for the
	// identical setting. Accept both so a value that works on one
	// backend doesn't silently stop mattering after switching to the
	// other.
	if v := req.Extras["es.heap"]; v != "" {
		return v
	}
	if v := req.Extras["heap"]; v != "" {
		return v
	}
	return "1g"
}

// buildDockerEnv assembles the env slice passed to the elasticsearch
// container. Extracted from dockerUp so the regression-guard tests can
// assert the publish_host / publish_port lines are present without
// having to start a real Docker daemon.
//
// Without `network.publish_host=127.0.0.1` + `http.publish_port=<host>`
// ES advertises its container-internal bridge IP (e.g.
// 172.17.0.4:9200) via `_nodes/http`. Any sniffing client
// (olivere/elastic, the official low-level clients with sniff=true,
// …) then dials 172.17.0.4 from the host and errors with `no
// Elasticsearch node available`. Pinning the publish host to the
// loopback the operator already hits, and overriding the HTTP publish
// port to the bough-allocated host port, makes sniff results
// host-reachable on a multi-worktree machine.
func buildDockerEnv(heap, hostPort string) []string {
	return []string{
		"discovery.type=single-node",
		"xpack.security.enabled=false",
		"cluster.routing.allocation.disk.threshold_enabled=false",
		"ES_JAVA_OPTS=-Xms" + heap + " -Xmx" + heap,
		// Bind on every container interface so docker's port-publish
		// (= -p 127.0.0.1:<hostPort>:9200) actually forwards from the
		// host. Without this ES dev-mode defaults to network.host=
		// _local_ (= container loopback only) and the host-side dial
		// hangs even though the container is up and healthy. Caught
		// by CI on linux runners (Docker Desktop on macOS papers over
		// this with its proxy layer, which is why v0.2.6 verified
		// locally but the conformance matrix surfaced it).
		"network.host=0.0.0.0",
		"network.publish_host=127.0.0.1",
		"http.publish_port=" + hostPort,
	}
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
		return fmt.Errorf("elasticsearch docker: invalid port %d (Ports=%v)", port, req.Ports)
	}
	if req.Datadir == "" {
		return errors.New("elasticsearch docker: datadir is required")
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
		return fmt.Errorf("elasticsearch docker: reuse check %s: %w", name, err)
	}
	if skip {
		return nil
	}

	if !dockerutil.IsPortFree(port) {
		return fmt.Errorf("elasticsearch docker: port %d already in use on 127.0.0.1 — stop the conflicting service or move bough's port range", port)
	}

	if err := dockerutil.PullIfMissing(ctx, cli, imageRef); err != nil {
		return fmt.Errorf("elasticsearch docker: pull %s: %w", imageRef, err)
	}

	// Pre-create the bind-mounted datadir and align permissions so the
	// elasticsearch process (which runs as uid 1000 in the official
	// image) can write to it.
	//
	// Three ownership cases the plugin handles:
	//
	//   - macOS Docker Desktop: VirtioFS hides the uid mismatch, plain
	//     0o755 works.
	//   - Linux + bough running as root: chown to 1000:0 succeeds and
	//     the directory is owned by the in-container user.
	//   - Linux + bough running as a non-root user (CI runners, most
	//     dev laptops): chown to 1000:0 fails with EPERM. Without a
	//     fallback the container exits with `AccessDeniedException`
	//     on its first index.create — the failure mode the bough
	//     conformance matrix surfaced on ubuntu-24.04 runners.
	//
	// The fallback flips the mode to 0o777 so the container can write
	// regardless of uid mismatch. This is wider than ideal for a
	// shared host, but per-worktree datadirs already live under a
	// user-private dir on a developer laptop; the trade-off is acceptable.
	if err := os.MkdirAll(req.Datadir, 0o755); err != nil {
		return fmt.Errorf("elasticsearch docker: mkdir datadir: %w", err)
	}
	if err := chownDatadirIfPossible(req.Datadir); err != nil {
		if cerr := os.Chmod(req.Datadir, 0o777); cerr != nil {
			return fmt.Errorf("elasticsearch docker: chmod datadir to 0o777 fallback: %w", cerr)
		}
	}

	heap := pickHeap(req)
	hostPort := fmt.Sprintf("%d", port)
	env := buildDockerEnv(heap, hostPort)
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
		Labels:       dockerutil.Labels(dockerEngine, imageRef, port),
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
	if err := dockerutil.StartOrCleanup(ctx, cli, resp.ID, "elasticsearch", port); err != nil {
		return err
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
