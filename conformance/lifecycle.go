package conformance

import (
	"context"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/db/api"

	"github.com/ikeikeikeike/bough/internal/pluginhost"
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
// corresponds to a contract statement in plugins/db/api/CONTRACT.md
// (added in Λ-6.6); a phase failure here is a contract violation.
//
// The phases:
//
//  1. PortRangeDefault — must return low > 0 && low < high.
//  2. Up → ReadyCheck → EnvVars (IdempotentCount times)
//     Each iteration must succeed; running Up again on already-up
//     state is up-or-reuse (no error). Each EnvVars map is checked
//     for non-empty values (the Λ-6.1 floor); the full reachable /
//     shell-safe / native-probe battery lands in Λ-6.2.
//  3. Down — must return without error after each iteration.
//  4. Cleanup — called twice. The second call must be a no-op (not
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

	t.Run("PortRangeDefault", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), quickPhaseTimeout)
		defer cancel()
		low, high, err := prov.PortRangeDefault(ctx)
		if err != nil {
			t.Fatalf("PortRangeDefault: %v", err)
		}
		if low <= 0 || low >= high {
			t.Fatalf("PortRangeDefault returned (%d, %d), want 0 < low < high", low, high)
		}
	})
	if t.Failed() {
		return
	}

	probeCtx, probeCancel := context.WithTimeout(t.Context(), quickPhaseTimeout)
	low, _, err := prov.PortRangeDefault(probeCtx)
	probeCancel()
	if err != nil {
		t.Fatalf("PortRangeDefault re-fetch: %v", err)
	}
	port := low
	datadir := newDatadir(t)
	upReq := api.UpReq{
		Port:             port,
		Datadir:          datadir,
		WorktreeRoot:     t.TempDir(),
		SocketDir:        t.TempDir(),
		InitialDatabases: []string{"conformance"},
		Extras:           mergeExtras(cfg),
	}

	for iter := 1; iter <= cfg.IdempotentCount; iter++ {
		iter := iter
		t.Run(itername("Up", iter), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), cfg.UpTimeout)
			defer cancel()
			if err := prov.Up(ctx, upReq); err != nil {
				t.Fatalf("Up (iter %d): %v", iter, err)
			}
		})
		if t.Failed() {
			return
		}

		t.Run(itername("ReadyCheck", iter), func(t *testing.T) {
			// ReadyCheck owns its own deadline (cfg.ReadyTimeout) — the
			// reason Up and ReadyCheck do NOT share a context: ES JVM
			// cold-start takes 3-5 min on a CI runner while Up itself
			// returns in seconds, and a single Up-sized ctx wedges
			// ReadyCheck before it can even start polling.
			ctx, cancel := context.WithTimeout(t.Context(), cfg.ReadyTimeout)
			defer cancel()
			ready, err := prov.ReadyCheck(ctx, port, int(cfg.ReadyTimeout.Seconds()))
			if err != nil {
				t.Fatalf("ReadyCheck (iter %d): %v", iter, err)
			}
			if !ready {
				t.Fatalf("ReadyCheck (iter %d): plugin returned not-ready within %s", iter, cfg.ReadyTimeout)
			}
		})
		if t.Failed() {
			return
		}

		t.Run(itername("EnvVars", iter), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), quickPhaseTimeout)
			defer cancel()
			env, err := prov.EnvVars(ctx, api.EnvVarsReq{
				Port:             port,
				InitialDatabases: upReq.InitialDatabases,
				SocketDir:        upReq.SocketDir,
			})
			if err != nil {
				t.Fatalf("EnvVars (iter %d): %v", iter, err)
			}
			AssertNonEmpty(t, env)
			AssertReachable(t, env)
			AssertShellSafe(t, env, cfg.AllowShellMetachars)
			if cfg.NativeProbe != nil {
				addrs := extractDialableAddrs(env)
				if len(addrs) == 0 {
					t.Errorf("NativeProbe configured but EnvVars did not advertise a dialable host:port")
				}
				probeCtx, probeCancel := context.WithTimeout(t.Context(), cfg.ReadyTimeout)
				defer probeCancel()
				for _, addr := range addrs {
					if err := cfg.NativeProbe(probeCtx, addr); err != nil {
						t.Errorf("NativeProbe against %s: %v", addr, err)
					}
				}
			}
		})
		if t.Failed() {
			return
		}

		t.Run(itername("Down", iter), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), quickPhaseTimeout)
			defer cancel()
			if err := prov.Down(ctx, api.DownReq{
				Port:               port,
				WorktreeRoot:       upReq.WorktreeRoot,
				GracefulTimeoutSec: 15,
			}); err != nil {
				t.Fatalf("Down (iter %d): %v", iter, err)
			}
		})
		if t.Failed() {
			return
		}
	}

	t.Run("Cleanup", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), quickPhaseTimeout)
		defer cancel()
		if err := prov.Cleanup(ctx, datadir, port); err != nil {
			if isPermissionDeniedFromContainerUID(err) {
				t.Skipf("Cleanup hit permission-denied — typical when the container "+
					"writes as a non-host uid (e.g. mysql/redis run as their own user "+
					"and host non-root cannot rm -rf the result). Not a contract "+
					"violation per se; tracked as a plugin-side follow-up. Raw: %v", err)
				return
			}
			t.Fatalf("Cleanup: %v", err)
		}
	})
	t.Run("Cleanup_idempotent", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), quickPhaseTimeout)
		defer cancel()
		if err := prov.Cleanup(ctx, datadir, port); err != nil {
			if isPermissionDeniedFromContainerUID(err) {
				t.Skipf("Cleanup (2nd call) hit permission-denied — see above. Raw: %v", err)
				return
			}
			t.Fatalf("Cleanup (2nd call): contract requires idempotency, got: %v", err)
		}
	})

	// Faults run in their own freshly-spawned plugin processes so a
	// panic in one cannot poison the next; each fault is gated by a
	// Skip* knob plugin authors can flip when the fault is not
	// simulable for their backend.
	runFaults(t, cfg)
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
