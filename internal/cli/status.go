package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/registry"
	"github.com/ikeikeikeike/bough/pkg/procutil"
	engineapi "github.com/ikeikeikeike/bough/plugins/engine/api"
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
			status := buildStatus(reg, cfg)
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
	// gateway / ...). It is derived from the YAML — the `backend:`
	// field, else `extras.backend`, else `<default> (default)` — not
	// read back from what a given worktree actually started, so it
	// reads the same for every worktree of that engine kind.
	Backend string `json:"backend,omitempty"`
}

func buildStatus(reg registry.Registry, cfg *config.Config) []statusEntry {
	// Engine kinds this registry has no row for are left out so their
	// Backend field stays empty.
	registeredKinds := make(map[string]bool)
	for _, kinds := range reg {
		for kind := range kinds {
			registeredKinds[engineKindFromRegistryKey(kind)] = true
		}
	}
	engineBackend := computeEngineBackends(cfg, registeredKinds)
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
// "postgres", "rabbitmq", ...) to the backend the create path would
// select for that engine: the dedicated `backend:` field, else the
// equally-authoritative `extras.backend`, else the default the host
// stamps in, annotated `<backend> (default)`. Non-engine ports and
// engine kinds absent from `registeredKinds` are left out of the map so
// the caller leaves their Backend field empty.
func computeEngineBackends(cfg *config.Config, registeredKinds map[string]bool) map[string]string {
	if cfg == nil {
		return nil
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
		default:
			out[eng.Kind] = engineapi.DefaultBackend + " (default)"
		}
	}
	return out
}
