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
//   - Container memory is capped via Docker's own `--memory` limit
//     (defaults to 2x heap, override via `extras["es.mem_limit"]="3g"`).
//     Elastic's own sizing guidance is heap <= 50% of container memory
//     with 1-2 GB of headroom for Lucene + the filesystem cache; 2x
//     heap satisfies that with a single knob. Without this limit a
//     single worktree's ES container can grow unbounded and, on a host
//     already running many parallel worktrees, tip the whole Docker
//     Desktop VM into its own OOM killer — which then kills containers
//     indiscriminately instead of the one actually misbehaving.
//   - HTTP readiness against `/` (not `_cluster/health?wait_for_status=
//     yellow`) because the root endpoint returns 200 once the cluster
//     is yellow-or-better — single-node ES is always yellow because
//     there is no replica to assign — and the request is cheaper.
//   - Stop timeout is 60s — ES translog flush + Lucene commit can run
//     long on a populated index.
//   - Plugins (`req.Plugins`, e.g. a third-party analyzer) install via
//     Elastic's own official `elasticsearch-plugins.yml` mechanism
//     (Docker-image-only, honoured since 7.16: the container compares
//     the file against what's already installed and adds/upgrades as
//     needed) rather than a hand-rolled shell wrapper — bough only has
//     to generate the file and bind-mount it in, ES owns the
//     idempotency. Auxiliary plugin config that mechanism does not
//     cover (e.g. an analyzer's dictionary file) bind-mounts from a
//     host directory via `extras["es.config_mount"]` (resolved like
//     kind: compose's `compose.file`: relative to the raw worktree
//     root, i.e. the directory containing every sibling repo — see
//     plugins/engine/compose/lifecycle.go) into
//     `extras["es.config_mount_target"]` (default: same basename under
//     the container's config dir).
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
	"path/filepath"
	"syscall"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"

	"github.com/ikeikeikeike/bough/pkg/dockerutil"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/docker/go-units"
	"gopkg.in/yaml.v3"
)

const (
	dockerEngine         = "elasticsearch"
	dockerDefaultImage   = "docker.elastic.co/elasticsearch/elasticsearch:7.17.29"
	dockerInternalHTTP   = "9200/tcp"
	dockerInternalTrans  = "9300/tcp"
	dockerDataDir        = "/usr/share/elasticsearch/data"
	dockerConfigDir      = "/usr/share/elasticsearch/config"
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

// defaultMemLimitMultiplier follows Elastic's own Docker sizing
// guidance (heap <= 50% of container memory, with 1-2 GB headroom for
// Lucene + the filesystem cache above the heap): doubling the heap
// satisfies that ratio with a single, easy-to-reason-about knob.
const defaultMemLimitMultiplier = 2

// pickMemoryLimitBytes returns the Docker container memory limit (the
// `--memory` equivalent) to enforce alongside the JVM heap. An explicit
// `extras["es.mem_limit"]` always wins; otherwise it defaults to
// defaultMemLimitMultiplier times the (already-validated) heap size.
// heap is guaranteed parseable by RAMInBytes too: validateHeap's
// pattern (`\d+[kmgKMG]?`) is a strict subset of what RAMInBytes
// accepts.
func pickMemoryLimitBytes(req *api.UpReq, heap string) (int64, error) {
	if v := req.Extras["es.mem_limit"]; v != "" {
		limit, err := units.RAMInBytes(v)
		if err != nil || limit <= 0 {
			return 0, fmt.Errorf("invalid es.mem_limit %q from extras (want e.g. 2g, 1500m): %w", v, err)
		}
		return limit, nil
	}
	heapBytes, err := units.RAMInBytes(heap)
	if err != nil || heapBytes <= 0 {
		return 0, fmt.Errorf("invalid heap %q for memory-limit derivation: %w", heap, err)
	}
	return heapBytes * defaultMemLimitMultiplier, nil
}

// pluginsYAMLFilename is the name Elastic's own Docker entrypoint looks
// for inside the config directory. See
// https://www.elastic.co/guide/en/elasticsearch/plugins/7.17/manage-plugins-using-configuration-file.html
const pluginsYAMLFilename = "elasticsearch-plugins.yml"

// pluginsYAMLDoc mirrors elasticsearch-plugins.yml's own shape 1:1 so
// api.PluginSpec marshals with no translation layer. Location omits
// empty so an official plugin's entry renders as bare `- id: ...`
// (matching Elastic's own examples) instead of a stray `location: ""`.
type pluginsYAMLDoc struct {
	Plugins []pluginsYAMLEntry `yaml:"plugins"`
}

type pluginsYAMLEntry struct {
	ID       string `yaml:"id"`
	Location string `yaml:"location,omitempty"`
}

// writePluginsYAML renders req.Plugins into elasticsearch-plugins.yml
// next to datadir (the same per-worktree .local/ scratch dir the other
// engine plugins already use for flake dirs / startup logs). Returns
// "" when there is nothing to declare, so an engine with no `plugins:`
// in its YAML behaves exactly as it did before this feature existed —
// dockerUp skips the bind-mount entirely rather than mounting an empty
// file over the image's own config dir.
func writePluginsYAML(datadir string, plugins []api.PluginSpec) (string, error) {
	if len(plugins) == 0 {
		return "", nil
	}
	doc := pluginsYAMLDoc{Plugins: make([]pluginsYAMLEntry, len(plugins))}
	for i, p := range plugins {
		doc.Plugins[i] = pluginsYAMLEntry{ID: p.ID, Location: p.Location}
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal %s: %w", pluginsYAMLFilename, err)
	}
	path := filepath.Join(filepath.Dir(datadir), pluginsYAMLFilename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", pluginsYAMLFilename, err)
	}
	return path, nil
}

// resolveConfigMount turns extras["es.config_mount"] into a Docker
// Binds entry ("<host>:<container>:ro"), or "" when unset. The host
// path resolves relative to the RAW worktree root (the directory
// containing every declared repository as a sibling) — req.WorktreeRoot
// itself is the engine-provider repo's own worktree path, one level too
// deep for a path like "auba-api/es-config/sudachi" that names a
// sibling repo. This mirrors kind: compose's identical resolution for
// compose.file (see plugins/engine/compose/lifecycle.go) so an
// operator who already understands that convention gets no surprises
// here.
//
// The container target defaults to the config dir plus the mount's own
// basename (e.g. ".../config/sudachi" for a host dir named "sudachi");
// extras["es.config_mount_target"] overrides it.
//
// Read-only, and deliberately without ensureDatadirWritableByContainer's
// chown/chmod dance: this mounts pre-fetched auxiliary plugin data
// (e.g. an analyzer dictionary) the elasticsearch process only ever
// reads, and a typical host-prepared directory (created via curl +
// unzip) is already world-readable, which is all uid 1000 needs for
// read access.
func resolveConfigMount(req *api.UpReq) (string, error) {
	src := req.Extras["es.config_mount"]
	if src == "" {
		return "", nil
	}
	if !filepath.IsAbs(src) {
		if req.WorktreeRoot == "" {
			return "", errors.New("es.config_mount is a relative path but WorktreeRoot is empty")
		}
		rawWorktreeRoot := filepath.Dir(req.WorktreeRoot)
		src = filepath.Join(rawWorktreeRoot, src)
	}
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("es.config_mount %s: %w", src, err)
	}
	target := req.Extras["es.config_mount_target"]
	if target == "" {
		target = dockerConfigDir + "/" + filepath.Base(src)
	}
	return src + ":" + target + ":ro", nil
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

// usingDockerBackend is the cheap self-detection used by Down /
// ReadyCheck when neither RPC carries an explicit backend hint. See
// dockerutil.IsBackendRunning for the shared stale-container-
// detection logic all four engine plugins share.
func usingDockerBackend(ctx context.Context, port int) bool {
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
	// in-container elasticsearch process (uid 1000) can write to it. See
	// ensureDatadirWritableByContainer for the ownership cases (root
	// chown, a reused datadir already owned by 1000, and the non-root
	// chmod-0o777 fallback).
	if err := os.MkdirAll(req.Datadir, 0o755); err != nil {
		return fmt.Errorf("elasticsearch docker: mkdir datadir: %w", err)
	}
	if err := ensureDatadirWritableByContainer(req.Datadir); err != nil {
		return err
	}

	heap := pickHeap(req)
	if err := validateHeap(heap); err != nil {
		return fmt.Errorf("elasticsearch docker: %w", err)
	}
	memLimitBytes, err := pickMemoryLimitBytes(req, heap)
	if err != nil {
		return fmt.Errorf("elasticsearch docker: %w", err)
	}
	pluginsYAMLPath, err := writePluginsYAML(req.Datadir, req.Plugins)
	if err != nil {
		return fmt.Errorf("elasticsearch docker: %w", err)
	}
	configMountBind, err := resolveConfigMount(req)
	if err != nil {
		return fmt.Errorf("elasticsearch docker: %w", err)
	}
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
	binds := []string{req.Datadir + ":" + dockerDataDir}
	if pluginsYAMLPath != "" {
		binds = append(binds, pluginsYAMLPath+":"+dockerConfigDir+"/"+pluginsYAMLFilename+":ro")
	}
	if configMountBind != "" {
		binds = append(binds, configMountBind)
	}
	hostCfg := &container.HostConfig{
		Binds:        binds,
		PortBindings: portBindings,
		Resources: container.Resources{
			Memory: memLimitBytes,
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

// esContainerUID is the uid the elasticsearch process runs as inside
// the official image (the `elasticsearch` user, uid 1000, gid 0).
const esContainerUID = 1000

// ensureDatadirWritableByContainer aligns the freshly-created,
// bind-mounted datadir so the in-container elasticsearch process
// (esContainerUID) can write to it. The bind-mount passes the host
// directory straight through, so its host-side ownership/mode is what
// decides whether uid 1000 can write.
//
// Cases, in order:
//
//   - chown to 1000:0 succeeds (bough runs as root, e.g. a CI runner):
//     the directory is owned by the in-container user; done. On macOS
//     Docker Desktop the VirtioFS proxy papers over the uid mismatch and
//     chown also succeeds.
//   - chown fails (EPERM on a non-root host) but the datadir is ALREADY
//     owned by uid 1000 — the common case for a datadir reused from a
//     prior run — so the container can already write and there is
//     nothing to fix. Widening the mode here is exactly what regressed:
//     a non-root host can neither chown NOR chmod a directory it does
//     not own, so the old chmod-0o777 fallback also EPERM'd and Up
//     hard-failed even though the datadir was writable by the container
//     (issue #74).
//   - otherwise widen the mode to 0o777 so uid 1000 can write despite
//     the ownership mismatch. Wider than ideal, but per-worktree
//     datadirs live under a user-private dir. Only if THAT also fails is
//     Up blocked, and then with an actionable error — the datadir is
//     owned by a third uid the host can neither chown nor chmod.
func ensureDatadirWritableByContainer(datadir string) error {
	if err := os.Chown(datadir, esContainerUID, 0); err == nil {
		return nil
	}
	if datadirOwnedBy(datadir, esContainerUID) {
		return nil
	}
	if err := os.Chmod(datadir, 0o777); err != nil {
		return fmt.Errorf("elasticsearch docker: cannot make datadir %q writable by the "+
			"in-container elasticsearch user (uid %d): chown and chmod both failed (%w). "+
			"It is likely a reused datadir owned by a different uid — remove it "+
			"(rm -rf %q) or run bough as a user that can chown it",
			datadir, esContainerUID, err, datadir)
	}
	return nil
}

// datadirOwnedBy reports whether datadir's owning uid is uid. Used to
// detect a reused datadir already owned by the container user, where
// neither chown nor chmod is needed (and, on a non-root host, neither is
// even possible).
func datadirOwnedBy(datadir string, uid uint32) bool {
	info, err := os.Stat(datadir)
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uid
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
