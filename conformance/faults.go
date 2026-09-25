package conformance

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	engineapi "github.com/ikeikeikeike/bough/plugins/engine/api"

	"github.com/ikeikeikeike/bough/internal/pluginhost"
)

// runFaults exercises deliberately-broken inputs and asserts that Up
// surfaces a real error for each. A plugin that swallows these
// failures silently is the worst failure mode: bough's allocator sees
// Up succeed, hands the host an unusable URL, and the downstream
// service crashes far from the actual root cause.
//
// Each fault is opt-out via the Skip* knobs in Config because some
// plugins genuinely cannot simulate them (e.g. a cluster-side
// provisioner that has no concept of a local socket cannot have its
// port preempted by `net.Listen`).
func runFaults(t *testing.T, cfg Config) {
	t.Helper()

	t.Run("Fault_PortConflict", func(t *testing.T) {
		if cfg.SkipPortConflict {
			t.Skip("SkipPortConflict")
		}
		runFaultPortConflict(t, cfg)
	})

	t.Run("Fault_ImagePullFailure", func(t *testing.T) {
		if cfg.SkipImagePullFailure {
			t.Skip("SkipImagePullFailure")
		}
		runFaultImagePullFailure(t, cfg)
	})
}

func runFaultPortConflict(t *testing.T, cfg Config) {
	prov, cleanup, port := spawnFreshAndPickPort(t, cfg)
	defer cleanup()

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skipf("could not bind sidecar listener on :%d: %v "+
			"(test cannot prove the plugin rejects port conflict)", port, err)
	}
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), cfg.UpTimeout)
	defer cancel()
	datadir := t.TempDir()
	upErr := prov.Up(ctx, &engineapi.UpReq{
		Ports:        []engineapi.PortSpec{{Role: cfg.MainPortRole, Port: port}},
		Datadir:      datadir,
		WorktreeRoot: t.TempDir(),
		SocketDir:    t.TempDir(),
		Extras:       mergeExtras(cfg),
	})
	if upErr == nil {
		// The plugin claimed success on a port we already hold.
		// Best-effort teardown to avoid leaving a real container
		// running, then fail the contract check.
		_ = prov.Down(ctx, &engineapi.DownReq{Ports: []int{port}, GracefulTimeoutSec: 5})
		_ = prov.Cleanup(ctx, datadir, []int{port})
		t.Errorf("Up on a port already held by a sidecar listener must fail; got nil error")
		return
	}
	t.Logf("Up surfaced port-conflict as: %v", upErr)
}

func runFaultImagePullFailure(t *testing.T, cfg Config) {
	prov, cleanup, port := spawnFreshAndPickPort(t, cfg)
	defer cleanup()

	extras := mergeExtras(cfg)
	// Force the plugin onto an image ref that cannot resolve.
	// We use a registry-prefix that the docker registry will reject
	// as "manifest unknown" rather than a random string that might
	// hit a 404 at the auth layer instead.
	extras["docker.image"] = "ghcr.io/ikeikeikeike/bough-conformance-does-not-exist:nope"

	datadir := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), cfg.UpTimeout)
	defer cancel()
	upErr := prov.Up(ctx, &engineapi.UpReq{
		Ports:        []engineapi.PortSpec{{Role: cfg.MainPortRole, Port: port}},
		Datadir:      datadir,
		WorktreeRoot: t.TempDir(),
		SocketDir:    t.TempDir(),
		Extras:       extras,
	})
	if upErr == nil {
		_ = prov.Down(ctx, &engineapi.DownReq{Ports: []int{port}, GracefulTimeoutSec: 5})
		_ = prov.Cleanup(ctx, datadir, []int{port})
		t.Errorf("Up with a non-existent image must fail; got nil error")
		return
	}
	// We intentionally do not pin error text — different registries
	// phrase "manifest unknown" / "pull failed" / "denied" differently.
	// The contract bound here is just "must surface a non-nil error".
	t.Logf("Up surfaced image-pull-failure as: %v", upErr)
}

// spawnFreshAndPickPort starts a brand-new plugin subprocess (each
// fault gets its own process so a panic in one cannot poison the
// next) and returns the configured main role's range Low as the port
// to drive Up with. Caller MUST defer the returned cleanup func.
//
// Faults always target a single role (port-conflict can only collide
// on one port at a time, image-pull is plugin-image-scoped) — so for
// multi-port plugins we still bind just the main role for these tests,
// which is what Config.MainPortRole identifies.
func spawnFreshAndPickPort(t *testing.T, cfg Config) (engineapi.EngineProvider, func(), int) {
	t.Helper()
	prov, kill, err := pluginhost.DiscoverFromBinary(cfg.PluginBinary)
	if err != nil {
		t.Fatalf("spawn plugin %q: %v", cfg.PluginBinary, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ranges, err := prov.PortRangeDefault(ctx)
	if err != nil {
		kill()
		t.Fatalf("PortRangeDefault: %v", err)
	}
	mainRange, ok := ranges[cfg.MainPortRole]
	if !ok {
		kill()
		t.Fatalf("PortRangeDefault did not declare the configured main role %q; "+
			"set conformance.Config.MainPortRole to one of the plugin's declared roles", cfg.MainPortRole)
	}
	// Route through the same probe-and-bind picker allocateRoles uses
	// (lifecycle.go) instead of the raw, unscanned Low — otherwise a
	// stray process already bound to Low (the exact class of CI flake
	// pickFreePort exists to dodge) makes Up() fail on a port
	// collision, and Fault_ImagePullFailure only asserts upErr != nil,
	// so it would false-pass without ever having exercised the fault it
	// exists to guard.
	return prov, kill, pickFreePort(mainRange.Low, mainRange.High, nil)
}
