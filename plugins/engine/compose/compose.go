//go:build darwin || linux

// Package compose implements the bough EngineProvider for kind:
// compose — a plugin that wraps an EXISTING docker-compose file and
// service instead of provisioning its own engine. The plugin binary
// spawned from cmd/bough-plugin-compose/main.go wraps this Provider
// as a Hashicorp go-plugin gRPC server, identically to the mysql /
// postgres / redis / elasticsearch siblings.
//
// Unlike the other four plugins, this one deliberately does not own
// the wrapped service's full lifecycle:
//
//   - Up shells out to `docker compose ... up -d <service>`, having
//     first rendered a worktree-scoped override file (never touching
//     the operator's own compose.yml) so the host-allocated port and a
//     bough-owned container_name are injected without any edits to the
//     checked-in file.
//   - Down stops AND removes only the target service's container
//     (never the sibling services that may share the same compose
//     file, and never `down`, which would also tear down the shared
//     project network/volumes). It re-derives the override + project
//     name from a sidecar state file Up recorded — DownReq carries no
//     Extras, so this information cannot otherwise survive between
//     RPCs. The container is removed (not just stopped) because its
//     name is deterministic by host port alone (bough-compose-<port>,
//     not project-scoped): leaving a stopped-but-present container
//     behind would collide with a LATER worktree's Up() once bough's
//     allocator reuses that same port.
//   - Cleanup is an intentional no-op: compose owns its own named
//     volumes, which Cleanup's (ctx, datadir, ports) signature has no
//     way to address, and reaching into an operator-owned compose
//     project's storage is arguably not this plugin's business anyway.
//   - ReadyCheck defaults to a plain TCP dial (extras
//     ["compose.ready_probe"] unset) because a generic wrapper cannot
//     know the wrapped service's protocol upfront. Set it explicitly
//     to "redis" / "postgres" / "mysql" / "http" for a real
//     protocol-level check — this matters most for "mysql": a bare
//     dial goes ready during the mysql image's --skip-networking
//     "temporary server" bootstrap phase, the exact race
//     plugins/engine/mysql/docker.go's dockerBackend.ReadyCheck already had to
//     fix for the bundled Docker backend (a host port's forwarding
//     accepts the TCP handshake from container start, regardless of
//     whether mysqld is listening yet).
//
// Known gap: Down never removes the compose project's own network
// (`docker network rm`), by design — scoping every operation to the
// single target service means this plugin never knows whether a
// sibling service (from the same compose file, under a different
// Engine entry) still needs that shared network. The network is
// harmless leftover cruft (no port/name collision risk, since network
// names are project-scoped) rather than a functional defect; an
// operator who wants it gone can `docker network prune` or remove it
// manually.
//
// darwin / linux only — the docker CLI's compose plugin targets both;
// Windows is out of scope for the same reason the other 4 plugins are.
package compose

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"

	"gopkg.in/yaml.v3"
)

// Provider implements api.EngineProvider for kind: compose.
type Provider struct {
	PortLow  int
	PortHigh int

	// mu guards cache, populated by Up() and read by EnvVars()/
	// ReadyCheck() — see state.go's cacheState/cachedState doc.
	mu    sync.Mutex
	cache map[int]*upState
}

// New returns a Provider with production defaults.
func New() *Provider { return &Provider{} }

const (
	// No engine-specific default makes sense for an arbitrary wrapped
	// service; kept clear of the four bundled plugins' own ranges
	// (mysql 42000-44999 / postgres 50000-52999 / redis 53000-55999 /
	// elasticsearch 56000-58999). .bough.yaml should always set
	// port_ranges.main explicitly for kind: compose.
	defaultPortLow  = 59000
	defaultPortHigh = 59999
)

// PortRangeDefault returns a placeholder range clear of the four
// bundled plugins' own ranges — see the defaultPortLow/High doc.
// Callers should always declare port_ranges.main explicitly in
// .bough.yaml for kind: compose rather than relying on this default.
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

// composeProjectName derives a deterministic, worktree-scoped
// `docker compose -p` project name from the worktree name and the
// compose file's declared path. Determinism matters twice over: Up
// and Down must independently derive the identical name (Down has no
// channel to receive whatever Up computed, besides the sidecar state
// file which stores it verbatim), and two different worktrees
// referencing the textually-identical compose file must NEVER derive
// the same name — compose's own up-or-reuse would otherwise silently
// hand one worktree a container actually owned by another.
//
// Compose project names must match `[a-z0-9][a-z0-9_-]*`; the result
// always starts with the literal "bough-" prefix, which is already a
// valid lowercase-alnum start regardless of how degenerate the inputs
// are.
func composeProjectName(worktreeName, file string) string {
	return "bough-" + sanitizeComposeToken(worktreeName) + "-" + sanitizeComposeToken(file)
}

// sanitizeComposeToken lowercases s and collapses every run of
// characters outside [a-z0-9] into a single '-', trimming leading/
// trailing dashes so adjacent sanitized tokens don't accumulate
// doubled separators.
func sanitizeComposeToken(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// overrideSpec is the input to renderOverride: the minimal set of
// facts needed to isolate one worktree's use of one compose service
// without editing the operator's own compose file.
type overrideSpec struct {
	Service    string
	TargetPort int
	HostPort   int
}

// composeOverrideDoc mirrors just enough of the compose file schema
// to express bough's port + container_name + label overrides via
// `docker compose -f <base> -f <override>`'s merge semantics.
type composeOverrideDoc struct {
	Services map[string]composeOverrideService `yaml:"services"`
}

type composeOverrideService struct {
	ContainerName string `yaml:"container_name"`
	// Ports is a yaml.Node (not []composePortSpec directly) so its Tag
	// can be forced to "!override" — the compose-spec merge directive
	// that replaces the base file's ports list wholesale. Without it,
	// `docker compose -f base -f override` APPENDS this plugin's port
	// mapping alongside whatever the operator's own compose file
	// already declared (e.g. a hardcoded "6379:6379"), publishing the
	// service on BOTH ports and defeating worktree isolation —
	// confirmed empirically against docker compose v5.0.2 before
	// landing this fix.
	Ports  yaml.Node         `yaml:"ports"`
	Labels map[string]string `yaml:"labels"`
}

// composePortSpec uses compose's long-form port syntax (target/
// published/protocol) rather than the short "host:container" string
// form specifically so the -f merge overrides by the `target` key
// instead of appending a second, colliding short-form mapping.
type composePortSpec struct {
	Target    int    `yaml:"target"`
	Published string `yaml:"published"`
	Protocol  string `yaml:"protocol"`
}

// com.bough.* label keys this plugin writes into the generated
// override. Write-only today (nothing reads them back), mirroring
// pkg/dockerutil/labels.go's own stated rationale: schema stability
// for a future label-based discovery tool, not a load-bearing
// dependency of this plugin's own Up/Down.
const (
	labelManaged        = "com.bough.managed"
	labelEngine         = "com.bough.engine"
	labelComposeService = "com.bough.compose-service"
	labelHostPort       = "com.bough.host-port"
)

// renderOverride generates the worktree-scoped compose override
// fragment for spec.Service: a fixed host port (never left to
// compose/Docker to assign, so bough's own allocator/registry stays
// authoritative) and a bough-owned container_name (compose's own
// <project>-<service>-N naming does not satisfy CONTRACT.md clause 1's
// `bough-<engine>-<port>` requirement, so this plugin forces it via
// the override rather than compose's default).
func renderOverride(spec overrideSpec) ([]byte, error) {
	var portsNode yaml.Node
	if err := portsNode.Encode([]composePortSpec{
		{Target: spec.TargetPort, Published: strconv.Itoa(spec.HostPort), Protocol: "tcp"},
	}); err != nil {
		return nil, fmt.Errorf("encode ports node: %w", err)
	}
	portsNode.Tag = "!override"

	doc := composeOverrideDoc{
		Services: map[string]composeOverrideService{
			spec.Service: {
				ContainerName: fmt.Sprintf("bough-compose-%d", spec.HostPort),
				Ports:         portsNode,
				Labels: map[string]string{
					labelManaged:        "true",
					labelEngine:         "compose",
					labelComposeService: spec.Service,
					labelHostPort:       strconv.Itoa(spec.HostPort),
				},
			},
		},
	}
	return yaml.Marshal(doc)
}
