package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ikeikeikeike/bough/internal/backend"
	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/registry"
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
				filepath.Join(monorepoRoot, cfg.Registry.Path),
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
	// gateway / ...). The value is the user-supplied `backend:` from
	// the YAML, or a `<detected> (auto)` annotation when the YAML left
	// it empty — matches the same Detect() the create path runs, so
	// operators can spot-check without re-running `bough create`.
	Backend string `json:"backend,omitempty"`
}

func buildStatus(reg registry.Registry, cfg *config.Config) []statusEntry {
	engineBackend := computeEngineBackends(cfg)
	var out []statusEntry
	for name, kinds := range reg {
		for kind, port := range kinds {
			pid := lsofListen(port)
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
// by the create path for that engine (the YAML override, or the auto-
// detect result annotated `<backend> (auto)`). Non-engine ports are
// absent from the map so the caller leaves their Backend field empty.
//
// Detect() is called at most once per status invocation; if no engine
// in the YAML leaves backend empty, Detect is skipped entirely.
func computeEngineBackends(cfg *config.Config) map[string]string {
	if cfg == nil {
		return nil
	}
	needsDetect := false
	for _, eng := range cfg.Engines {
		if eng.Backend == "" {
			needsDetect = true
			break
		}
	}
	var detected string
	if needsDetect {
		// 3s cap so an unresponsive nix/docker daemon does not stall
		// `bough status`; on timeout we leave detected empty and the
		// affected entries get the "unresolved" placeholder.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if v, err := backend.Detect(ctx); err == nil {
			detected = v
		}
	}
	out := make(map[string]string, len(cfg.Engines))
	for _, eng := range cfg.Engines {
		if eng.Backend != "" {
			out[eng.Kind] = eng.Backend
		} else if detected != "" {
			out[eng.Kind] = detected + " (auto)"
		} else {
			out[eng.Kind] = "unresolved"
		}
	}
	return out
}

// lsofListen returns the PID of whichever process holds the TCP
// listener on `port`, or 0 when nothing is listening. Mirror of the
// helper in plugins/engine/mysql/mysql.go.
func lsofListen(port int) int {
	out, err := exec.Command("lsof", fmt.Sprintf("-tiTCP:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0
	}
	if i := strings.IndexAny(s, "\n\t "); i >= 0 {
		s = s[:i]
	}
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return pid
}
