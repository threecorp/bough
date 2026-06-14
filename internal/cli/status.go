package cli

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

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
			status := buildStatus(reg)
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(status)
			}
			for _, s := range status {
				fmt.Fprintf(cmd.OutOrStdout(), "%s/%s :: port=%d, listening=%v, pid=%d\n",
					s.Name, s.Kind, s.Port, s.Listening, s.PID)
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
}

func buildStatus(reg registry.Registry) []statusEntry {
	var out []statusEntry
	for name, kinds := range reg {
		for kind, port := range kinds {
			pid := lsofListen(port)
			out = append(out, statusEntry{
				Name: name, Kind: kind, Port: port,
				Listening: pid > 0, PID: pid,
			})
		}
	}
	return out
}

// lsofListen returns the PID of whichever process holds the TCP
// listener on `port`, or 0 when nothing is listening. Mirror of the
// helper in plugins/db/mysql/mysql.go.
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
