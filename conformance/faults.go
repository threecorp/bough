package conformance

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/db/api"

	"github.com/ikeikeikeike/bough/internal/pluginhost"
)

// runFaults exercises three deliberately-broken inputs and asserts
// that Up surfaces a real error for each. A plugin that swallows
// these failures silently is the worst failure mode: bough's
// allocator sees Up succeed, hands the host an unusable URL, and the
// downstream service crashes far from the actual root cause.
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

	t.Run("Fault_DatadirPermission", func(t *testing.T) {
		if cfg.SkipDatadirPermission {
			t.Skip("SkipDatadirPermission")
		}
		runFaultDatadirPermission(t, cfg)
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
	upErr := prov.Up(ctx, api.UpReq{
		Port:         port,
		Datadir:      datadir,
		WorktreeRoot: t.TempDir(),
		SocketDir:    t.TempDir(),
		Extras:       mergeExtras(cfg),
	})
	if upErr == nil {
		// The plugin claimed success on a port we already hold.
		// Best-effort teardown to avoid leaving a real container
		// running, then fail the contract check.
		_ = prov.Down(ctx, api.DownReq{Port: port, GracefulTimeoutSec: 5})
		_ = prov.Cleanup(ctx, datadir, port)
		t.Errorf("Up on a port already held by a sidecar listener must fail; got nil error")
		return
	}
	t.Logf("Up surfaced port-conflict as: %v", upErr)
}

func runFaultDatadirPermission(t *testing.T, cfg Config) {
	if os.Getuid() == 0 {
		t.Skip("running as root — chmod 0o000 does not block writes; cannot test this path")
	}

	prov, cleanup, port := spawnFreshAndPickPort(t, cfg)
	defer cleanup()

	parent := t.TempDir()
	faultDir := filepath.Join(parent, "perm-fault")
	if err := os.MkdirAll(faultDir, 0o755); err != nil {
		t.Fatalf("mkdir fault dir: %v", err)
	}
	// Restore on cleanup so t.TempDir's RemoveAll can succeed.
	t.Cleanup(func() { _ = os.Chmod(faultDir, 0o700) })
	if err := os.Chmod(faultDir, 0o000); err != nil {
		t.Skipf("chmod fault dir 0o000: %v (cannot test this path)", err)
	}

	datadir := filepath.Join(faultDir, "data")
	ctx, cancel := context.WithTimeout(t.Context(), cfg.UpTimeout)
	defer cancel()
	upErr := prov.Up(ctx, api.UpReq{
		Port:         port,
		Datadir:      datadir,
		WorktreeRoot: t.TempDir(),
		SocketDir:    t.TempDir(),
		Extras:       mergeExtras(cfg),
	})
	if upErr == nil {
		_ = prov.Down(ctx, api.DownReq{Port: port, GracefulTimeoutSec: 5})
		_ = prov.Cleanup(ctx, datadir, port)
		t.Errorf("Up with un-writable datadir parent must fail; got nil error")
		return
	}
	t.Logf("Up surfaced datadir-permission as: %v", upErr)
}

func runFaultImagePullFailure(t *testing.T, cfg Config) {
	prov, cleanup, port := spawnFreshAndPickPort(t, cfg)
	defer cleanup()

	extras := make(map[string]string, len(cfg.Extras)+1)
	for k, v := range cfg.Extras {
		extras[k] = v
	}
	// Force the plugin onto an image ref that cannot resolve.
	// We use a registry-prefix that the docker registry will reject
	// as "manifest unknown" rather than a random string that might
	// hit a 404 at the auth layer instead.
	extras["docker.image"] = "ghcr.io/ikeikeikeike/bough-conformance-does-not-exist:nope"

	datadir := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), cfg.UpTimeout)
	defer cancel()
	upErr := prov.Up(ctx, api.UpReq{
		Port:         port,
		Datadir:      datadir,
		WorktreeRoot: t.TempDir(),
		SocketDir:    t.TempDir(),
		Extras:       extras,
	})
	if upErr == nil {
		_ = prov.Down(ctx, api.DownReq{Port: port, GracefulTimeoutSec: 5})
		_ = prov.Cleanup(ctx, datadir, port)
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
// next) and returns its PortRangeDefault low end as the port to
// drive Up with. Caller MUST defer the returned cleanup func.
func spawnFreshAndPickPort(t *testing.T, cfg Config) (api.DBProvider, func(), int) {
	t.Helper()
	prov, kill, err := pluginhost.DiscoverFromBinary(cfg.PluginBinary)
	if err != nil {
		t.Fatalf("spawn plugin %q: %v", cfg.PluginBinary, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	low, _, err := prov.PortRangeDefault(ctx)
	if err != nil {
		kill()
		t.Fatalf("PortRangeDefault: %v", err)
	}
	return prov, kill, low
}
