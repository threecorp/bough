package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ikeikeikeike/bough/internal/backend"
	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/registry"
	"github.com/ikeikeikeike/bough/pkg/procutil"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the registry alongside lsof listen state for each port",
		RunE: func(cmd *cobra.Command, _ []string) error {
			monorepoRoot, cfg, err := loadConfigAndRoot(cmd, "")
			if err != nil {
				return err
			}
			store := registry.NewStore(
				resolveRegistryPath(monorepoRoot, cfg.Registry.Path),
				cfg.Registry.BackupDir,
			)
			reg, err := store.Load()
			if err != nil {
				return err
			}
			status := buildStatus(cmd.Context(), reg, cfg)
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(status)
			}
			for _, s := range status {
				if s.Backend != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s/%s :: port=%d, listening=%v, pid=%d, backend=%s\n",
						s.Name, s.Kind, s.Port, s.Listening, s.PID, s.Backend)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "%s/%s :: port=%d, listening=%v, pid=%d\n",
						s.Name, s.Kind, s.Port, s.Listening, s.PID)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable JSON instead of human-readable lines")
	return cmd
}

type statusEntry struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Port      int    `json:"port"`
	Listening bool   `json:"listening"`
	PID       int    `json:"pid,omitempty"`
	// Backend is the lifecycle runtime for engine kinds (mysql /
	// postgres / redis / elasticsearch / rabbitmq / ...). Empty for the
	// non-engine port kinds the host allocates alongside (api /
	// gateway / ...). The value is the user-supplied `backend:` (or
	// `extras.backend`) from the YAML when explicitly set — accurate
	// per worktree. When the YAML leaves it on auto-detect, this is
	// instead a single `<detected> (auto)` guess from ONE Detect() call
	// made right now on the host, stamped onto every worktree of that
	// engine kind — it is NOT read back from what that specific
	// worktree's `bough create` actually chose (no per-worktree backend
	// choice is persisted anywhere), so it can be wrong for a worktree
	// created under different host conditions (e.g. before Docker was
	// installed, or before/after a nix install).
	Backend string `json:"backend,omitempty"`
}

func buildStatus(ctx context.Context, reg registry.Registry, cfg *config.Config) []statusEntry {
	// Only probe backends for engine kinds this registry actually has
	// a row for — a config declaring mysql/redis/es but a registry
	// with zero worktrees yet must not pay Detect()'s daemon-probe
	// latency on every `bough status` call.
	registeredKinds := make(map[string]bool)
	for _, kinds := range reg {
		for kind := range kinds {
			registeredKinds[engineKindFromRegistryKey(kind)] = true
		}
	}
	engineBackend := computeEngineBackends(ctx, cfg, registeredKinds)
	var out []statusEntry
	for name, kinds := range reg {
		for kind, port := range kinds {
			pid := procutil.LsofListener(port)
			out = append(out, statusEntry{
				Name: name, Kind: kind, Port: port,
				Listening: pid > 0, PID: pid,
				// Registry stores engine entries under composite keys
				// `<kind>.<role>` (e.g. `mysql.main`), so split on the
				// first dot before looking up the backend keyed by raw
				// engine kind. Non-engine kinds (api / gateway) have no
				// dot and pass through unchanged.
				Backend: engineBackend[engineKindFromRegistryKey(kind)],
			})
		}
	}
	return out
}

// engineKindFromRegistryKey extracts the engine kind from a registry
// composite key. v0.4 registry keys engine entries as `<kind>.<role>`;
// non-engine port kinds (api / gateway / view / ...) carry no role
// suffix. Legacy v0.3 keys (no dot) pass through.
func engineKindFromRegistryKey(key string) string {
	if i := strings.IndexByte(key, '.'); i >= 0 {
		return key[:i]
	}
	return key
}

// computeEngineBackends returns a map from engine kind ("mysql",
// "postgres", "rabbitmq", ...) to the backend that would be selected
// by the create path for that engine (the YAML override — either the
// dedicated `backend:` field or the equally-authoritative
// `extras.backend` — or the auto-detect result annotated
// `<backend> (auto)`). Non-engine ports and engine kinds absent from
// `registeredKinds` are left out of the map so the caller leaves
// their Backend field empty and Detect() is never called for a kind
// the registry has no row for.
//
// Detect() is called at most once per status invocation, sharing
// backend.Detect's own internal detectTimeout cap under the caller's
// ctx (honors Ctrl+C/SIGTERM like every other bough subcommand) — the
// same call create.go's detectBackendIfNeeded makes, so a cold-store
// probe that create tolerates does not spuriously report "unresolved"
// here under a shorter, disconnected deadline.
func computeEngineBackends(ctx context.Context, cfg *config.Config, registeredKinds map[string]bool) map[string]string {
	if cfg == nil {
		return nil
	}
	needsDetect := false
	for _, eng := range cfg.Engines {
		if !registeredKinds[eng.Kind] {
			continue
		}
		if eng.Backend == "" && eng.Extras["backend"] == "" {
			needsDetect = true
			break
		}
	}
	var detected string
	var detectErr error
	if needsDetect {
		detected, detectErr = backend.Detect(ctx)
	}
	out := make(map[string]string, len(cfg.Engines))
	for _, eng := range cfg.Engines {
		if !registeredKinds[eng.Kind] {
			continue
		}
		switch {
		case eng.Backend != "":
			out[eng.Kind] = eng.Backend
		case eng.Extras["backend"] != "":
			out[eng.Kind] = eng.Extras["backend"]
		case detected != "":
			out[eng.Kind] = detected + " (auto)"
		case detectErr != nil:
			out[eng.Kind] = fmt.Sprintf("unresolved (%v)", detectErr)
		default:
			out[eng.Kind] = "unresolved"
		}
	}
	return out
}
