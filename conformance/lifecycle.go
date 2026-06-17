package conformance

import (
	"context"
	"strconv"
	"testing"

	api "github.com/ikeikeikeike/bough/plugins/db/api"

	"github.com/ikeikeikeike/bough/internal/pluginhost"
)

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

	ctx, cancel := context.WithTimeout(t.Context(), cfg.UpTimeout)
	defer cancel()

	t.Run("PortRangeDefault", func(t *testing.T) {
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

	low, _, err := prov.PortRangeDefault(ctx)
	if err != nil {
		t.Fatalf("PortRangeDefault re-fetch: %v", err)
	}
	port := low
	datadir := t.TempDir()
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
			if err := prov.Up(ctx, upReq); err != nil {
				t.Fatalf("Up (iter %d): %v", iter, err)
			}
		})
		if t.Failed() {
			return
		}

		t.Run(itername("ReadyCheck", iter), func(t *testing.T) {
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
				for _, addr := range addrs {
					if err := cfg.NativeProbe(ctx, addr); err != nil {
						t.Errorf("NativeProbe against %s: %v", addr, err)
					}
				}
			}
		})
		if t.Failed() {
			return
		}

		t.Run(itername("Down", iter), func(t *testing.T) {
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
		if err := prov.Cleanup(ctx, datadir, port); err != nil {
			t.Fatalf("Cleanup: %v", err)
		}
	})
	t.Run("Cleanup_idempotent", func(t *testing.T) {
		if err := prov.Cleanup(ctx, datadir, port); err != nil {
			t.Fatalf("Cleanup (2nd call): contract requires idempotency, got: %v", err)
		}
	})

	// Faults run in their own freshly-spawned plugin processes so a
	// panic in one cannot poison the next; each fault is gated by a
	// Skip* knob plugin authors can flip when the fault is not
	// simulable for their backend.
	runFaults(t, cfg)
}

// mergeExtras lifts cfg.Extras and stamps in `docker.image` from
// cfg.Image so plugins that key on either convention see it. The
// mutation is on a copy — Run must not mutate the caller's Config.
func mergeExtras(cfg Config) map[string]string {
	out := make(map[string]string, len(cfg.Extras)+1)
	for k, v := range cfg.Extras {
		out[k] = v
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
