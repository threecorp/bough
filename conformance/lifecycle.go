package conformance

import (
	"context"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"time"

	engineapi "github.com/ikeikeikeike/bough/plugins/engine/api"

	"github.com/ikeikeikeike/bough/internal/pluginhost"
	"github.com/ikeikeikeike/bough/pkg/dockerutil"
)

// quickPhaseTimeout bounds Down / Cleanup / EnvVars / PortRangeDefault
// — phases that should not legitimately take more than seconds even on
// a cold CI runner. Up uses cfg.UpTimeout; ReadyCheck uses
// cfg.ReadyTimeout (which is what motivates phase-scoped contexts in
// the first place: ES needs 5 min for JVM cold-start but Up itself
// only needs ~5 s, and a single 120 s ctx wedges ReadyCheck before it
// can even start polling).
const quickPhaseTimeout = 60 * time.Second

// runLifecycle drives the contract-defined phases against a freshly
// spawned plugin binary. It is the heart of Run: every phase below
// corresponds to a clause in plugins/engine/api/CONTRACT.md; a phase
// failure here is a contract violation.
//
// The phases:
//
//  1. PortRangeDefault — every role's range has 0 < Low < High.
//  2. Up → ReadyCheck → EnvVars → Down (IdempotentCount times).
//     Running Up again on already-up state is up-or-reuse (no error);
//     each EnvVars map is checked for non-empty / reachable / shell-
//     safe values, plus an optional NativeProbe round-trip.
//  3. Cleanup — called twice. The second call must be a no-op (not
//     an error), enforcing the idempotency clause of the contract.
//
// A single t.Fatalf at any phase aborts the rest — there is no value
// in checking EnvVars when Up never returned.
func runLifecycle(t *testing.T, cfg Config) {
	t.Helper()

	prov, cleanup, err := pluginhost.DiscoverFromBinary(cfg.PluginBinary)
	if err != nil {
		t.Fatalf("conformance: spawn plugin %q: %v", cfg.PluginBinary, err)
	}
	t.Cleanup(cleanup)

	ranges := assertPortRangeDefault(t, prov)
	if t.Failed() {
		return
	}
	if _, ok := ranges[cfg.MainPortRole]; !ok {
		t.Fatalf("PortRangeDefault did not declare the configured main role %q (got roles %v); "+
			"set Config.MainPortRole to one of the declared roles",
			cfg.MainPortRole, keysOfPortRanges(ranges))
	}

	// One PortSpec per declared role, each allocated the role's Low
	// port. Multi-port engines get every role bound; single-port
	// engines see exactly one PortSpec with Role="main".
	ports, portInts, mainPort := allocateRoles(ranges, cfg.MainPortRole)

	datadir := newDatadir(t)
	upReq := &engineapi.UpReq{
		Ports:            ports,
		Datadir:          datadir,
		WorktreeRoot:     t.TempDir(),
		SocketDir:        t.TempDir(),
		InitialResources: []engineapi.ResourceSpec{{Type: "database", Name: "conformance"}},
		Extras:           mergeExtras(cfg),
	}

	for iter := 1; iter <= cfg.IdempotentCount; iter++ {
		if !runOneIteration(t, prov, upReq, portInts, mainPort, cfg, iter) {
			return
		}
	}

	assertCleanup(t, prov, datadir, portInts)

	// Faults run in their own freshly-spawned plugin processes so a
	// panic in one cannot poison the next; each fault is gated by a
	// Skip* knob plugin authors flip when the fault is not simulable.
	runFaults(t, cfg)
}

// assertPortRangeDefault runs the PortRangeDefault sub-test and
// returns the role→range map for the caller to use downstream.
func assertPortRangeDefault(t *testing.T, prov engineapi.EngineProvider) map[string]engineapi.PortRange {
	t.Helper()
	var ranges map[string]engineapi.PortRange
	t.Run("PortRangeDefault", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), quickPhaseTimeout)
		defer cancel()
		r, err := prov.PortRangeDefault(ctx)
		if err != nil {
			t.Fatalf("PortRangeDefault: %v", err)
		}
		if len(r) == 0 {
			t.Fatalf("PortRangeDefault returned an empty map; want at least one role")
		}
		for role, pr := range r {
			if pr.Low <= 0 || pr.Low >= pr.High {
				t.Fatalf("PortRangeDefault[%q] = (low=%d, high=%d), want 0 < low < high",
					role, pr.Low, pr.High)
			}
		}
		ranges = r
	})
	return ranges
}

// runOneIteration drives Up → ReadyCheck → EnvVars → Down once.
// Returns false on a fatal sub-test failure so runLifecycle can stop
// the loop without dragging the operator through phases that cannot
// pass on a never-up plugin.
func runOneIteration(
	t *testing.T,
	prov engineapi.EngineProvider,
	upReq *engineapi.UpReq,
	portInts []int,
	mainPort int,
	cfg Config,
	iter int,
) bool {
	t.Helper()
	t.Run(itername("Up", iter), func(t *testing.T) { runUpPhase(t, prov, upReq, cfg, iter) })
	if t.Failed() {
		return false
	}
	t.Run(itername("ReadyCheck", iter), func(t *testing.T) {
		runReadyCheckPhase(t, prov, portInts, mainPort, cfg, iter)
	})
	if t.Failed() {
		return false
	}
	t.Run(itername("EnvVars", iter), func(t *testing.T) { runEnvVarsPhase(t, prov, upReq, cfg, iter) })
	if t.Failed() {
		return false
	}
	t.Run(itername("Down", iter), func(t *testing.T) { runDownPhase(t, prov, upReq.WorktreeRoot, portInts, iter) })
	return !t.Failed()
}

func runUpPhase(t *testing.T, prov engineapi.EngineProvider, upReq *engineapi.UpReq, cfg Config, iter int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), cfg.UpTimeout)
	defer cancel()
	if err := prov.Up(ctx, upReq); err != nil {
		t.Fatalf("Up (iter %d): %v", iter, err)
	}
}

func runReadyCheckPhase(t *testing.T, prov engineapi.EngineProvider, ports []int, mainPort int, cfg Config, iter int) {
	t.Helper()
	// ReadyCheck owns its own deadline (cfg.ReadyTimeout) — the reason
	// Up and ReadyCheck do NOT share a context: ES JVM cold-start
	// takes 3-5 min on a CI runner while Up itself returns in seconds,
	// and a single Up-sized ctx wedges ReadyCheck before it can even
	// start polling.
	ctx, cancel := context.WithTimeout(t.Context(), cfg.ReadyTimeout)
	defer cancel()
	ready, err := prov.ReadyCheck(ctx, ports, int(cfg.ReadyTimeout.Seconds()))
	if err != nil {
		t.Fatalf("ReadyCheck (iter %d): %v", iter, err)
	}
	if !ready {
		t.Fatalf("ReadyCheck (iter %d): plugin returned not-ready within %s (main port %d)",
			iter, cfg.ReadyTimeout, mainPort)
	}
}

func runEnvVarsPhase(t *testing.T, prov engineapi.EngineProvider, upReq *engineapi.UpReq, cfg Config, iter int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), quickPhaseTimeout)
	defer cancel()
	env, err := prov.EnvVars(ctx, &engineapi.EnvVarsReq{
		Ports:            upReq.Ports,
		InitialResources: upReq.InitialResources,
		SocketDir:        upReq.SocketDir,
	})
	if err != nil {
		t.Fatalf("EnvVars (iter %d): %v", iter, err)
	}
	AssertNonEmpty(t, env)
	AssertReachable(t, env)
	AssertShellSafe(t, env, cfg.AllowShellMetachars)
	runNativeProbeIfConfigured(t, env, cfg)
}

// runNativeProbeIfConfigured dispatches every dialable addr in env
// through cfg.NativeProbe and surfaces protocol-level reachability
// failures. Silent when cfg.NativeProbe is nil — the v0.2.6 guard
// only matters when the plugin author wires it up.
func runNativeProbeIfConfigured(t *testing.T, env map[string]string, cfg Config) {
	t.Helper()
	if cfg.NativeProbe == nil {
		return
	}
	addrs := extractDialableAddrs(env)
	if len(addrs) == 0 {
		t.Errorf("NativeProbe configured but EnvVars did not advertise a dialable host:port")
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), cfg.ReadyTimeout)
	defer cancel()
	for _, addr := range addrs {
		if err := cfg.NativeProbe(ctx, addr); err != nil {
			t.Errorf("NativeProbe against %s: %v", addr, err)
		}
	}
}

func runDownPhase(t *testing.T, prov engineapi.EngineProvider, worktreeRoot string, ports []int, iter int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), quickPhaseTimeout)
	defer cancel()
	err := prov.Down(ctx, &engineapi.DownReq{
		Ports:              ports,
		WorktreeRoot:       worktreeRoot,
		GracefulTimeoutSec: 15,
	})
	if err != nil {
		t.Fatalf("Down (iter %d): %v", iter, err)
	}
}

// assertCleanup runs the two terminal Cleanup sub-tests (initial +
// idempotent). The container-uid permission case is a Skip, not a
// Fail, because the underlying chown follow-up is plugin-side.
func assertCleanup(t *testing.T, prov engineapi.EngineProvider, datadir string, ports []int) {
	t.Helper()
	t.Run("Cleanup", func(t *testing.T) {
		runCleanupCall(t, prov, datadir, ports, "Cleanup",
			"Cleanup hit permission-denied — typical when the container "+
				"writes as a non-host uid (e.g. mysql/redis run as their own user "+
				"and host non-root cannot rm -rf the result). Not a contract "+
				"violation per se; tracked as a plugin-side follow-up. Raw: %v")
	})
	t.Run("Cleanup_idempotent", func(t *testing.T) {
		runCleanupCall(t, prov, datadir, ports, "Cleanup (2nd call)",
			"Cleanup (2nd call) hit permission-denied — see above. Raw: %v")
	})
}

func runCleanupCall(t *testing.T, prov engineapi.EngineProvider, datadir string, ports []int, label, skipFmt string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), quickPhaseTimeout)
	defer cancel()
	err := prov.Cleanup(ctx, datadir, ports)
	if err == nil {
		return
	}
	if isPermissionDeniedFromContainerUID(err) {
		t.Skipf(skipFmt, err)
		return
	}
	if label == "Cleanup (2nd call)" {
		t.Fatalf("%s: contract requires idempotency, got: %v", label, err)
	}
	t.Fatalf("%s: %v", label, err)
}

// mergeExtras lifts cfg.Extras and stamps in `backend=docker` plus
// `docker.image=<cfg.Image>` so plugins that branch on `extras["backend"]`
// (bough's hybrid selector convention since Λ-5b) take the docker
// path even when `nix` is on PATH. The conformance suite is the
// docker-backend contract guard; verifying the nix-services-flake
// path is a separate follow-up. Callers may pass `backend: nix`
// explicitly in cfg.Extras to override.
//
// The mutation is on a copy — Run must not mutate the caller's Config.
func mergeExtras(cfg Config) map[string]string {
	out := make(map[string]string, len(cfg.Extras)+2)
	for k, v := range cfg.Extras {
		out[k] = v
	}
	if _, ok := out["backend"]; !ok {
		out["backend"] = "docker"
	}
	if cfg.Image != "" {
		out["docker.image"] = cfg.Image
	}
	return out
}

func itername(phase string, iter int) string {
	if iter == 1 {
		return phase
	}
	return phase + "_iter" + strconv.Itoa(iter)
}

// keysOfPortRanges lists the role names declared by PortRangeDefault,
// used only when the suite needs to diagnose a missing main role.
func keysOfPortRanges(m map[string]engineapi.PortRange) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// allocateRoles converts a PortRangeDefault map into the three shapes
// the lifecycle test needs downstream:
//
//   - `ports []PortSpec`  → passed to Up / EnvVars verbatim, one
//     entry per declared role.
//   - `portInts []int`    → flattened to []int for ReadyCheck / Down
//     / Cleanup (which take the same set without role labels).
//   - `mainPort int`      → the port allocated for `mainRole`, used
//     for diagnostic error messages only.
//
// The role-port assignment is the Low end of each role's range. The
// host's production allocator does deterministic crc32(name|role)
// hashing, but the conformance suite is a single per-process test —
// scanning from Low keeps the test reproducible most of the time
// without dragging the allocator into the picture.
//
// CI runners occasionally have another process bound to Low (= the
// 50000-postgres-collision recurrence on GHA ubuntu-24.04 runners
// where another postgres instance happens to be on 50000). To make
// the suite stable against that, allocateRoles scans Low → High and
// uses the first port that binds. Empty range or all-busy range
// returns Low to preserve the v0.7 error path (= "port already in
// use" surfaces from Up rather than from the allocator).
//
// `mainRole` is assumed to exist in ranges; the caller guards that
// invariant before this is reached.
func allocateRoles(ranges map[string]engineapi.PortRange, mainRole string) ([]engineapi.PortSpec, []int, int) {
	ports := make([]engineapi.PortSpec, 0, len(ranges))
	portInts := make([]int, 0, len(ranges))
	mainPort := 0
	// claimed tracks ports already handed to an earlier role in this
	// same call so two roles with overlapping PortRangeDefault ranges
	// (e.g. a multi-port engine's AMQP + Management ranges) can never
	// be assigned the identical port — pickFreePort's own probe-release
	// would otherwise report the same just-vacated port free to both.
	claimed := make(map[int]bool, len(ranges))
	for role, pr := range ranges {
		port := pickFreePort(pr.Low, pr.High, claimed)
		claimed[port] = true
		ports = append(ports, engineapi.PortSpec{Role: role, Port: port})
		portInts = append(portInts, port)
		if role == mainRole {
			mainPort = port
		}
	}
	return ports, portInts, mainPort
}

// portScanAttempts bounds how many consecutive candidate ports
// pickFreePort probes from low before giving up — conformance ranges
// are wide enough that more busy/claimed ports in a row than this
// indicates something else is wrong.
const portScanAttempts = 64

// pickFreePort scans [low, high] for the first port that is free on
// 127.0.0.1 (per dockerutil.IsPortFree) and not already in claimed
// (ports allocateRoles already handed to an earlier role in the same
// call; pass nil when there is only one role, as faults.go does).
//
// IsPortFree is a best-effort, momentary probe — the caller must
// still detect the backend's "port already allocated" error when the
// engine actually binds, same caveat as IsPortFree's own doc comment.
//
// Falls back to low when no free port is found so the caller still
// surfaces the (deterministic) failure path through the engine
// rather than swallowing the contention here.
func pickFreePort(low, high int, claimed map[int]bool) int {
	if high <= low {
		return low
	}
	limit := low + portScanAttempts
	if limit > high {
		limit = high
	}
	for p := low; p <= limit; p++ {
		if claimed[p] {
			continue
		}
		if dockerutil.IsPortFree(p) {
			return p
		}
	}
	return low
}

// isPermissionDeniedFromContainerUID recognises the
// "host non-root cannot unlink files the container wrote as a non-
// host uid" case so the conformance suite can skip rather than fail.
// macOS Docker Desktop's VirtioFS hides the uid mismatch so this only
// fires on Linux runners, which is where it most matters.
//
// This is a Skip, not a Pass, so the plugin-side follow-up (chown
// via `docker run --rm -v datadir:/data alpine chown` or similar)
// stays visible.
func isPermissionDeniedFromContainerUID(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	// The plugin wraps os.RemoveAll's PathError; check the textual
	// signature too since `errors.Is` does not traverse fmt.Errorf %w
	// chains that aren't explicit wrap points.
	return strings.Contains(err.Error(), "permission denied")
}
