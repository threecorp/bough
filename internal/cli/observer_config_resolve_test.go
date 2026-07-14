package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// TestResolveObserverConfig_LegacyConfigFallback is the regression test
// for the missing .worktree-isolation.yaml fallback: dispatchObserverAutostart
// used to build its config path with an ad hoc filepath.Join(root,
// ".bough.yaml"), which silently found nothing on a monorepo that has not
// renamed its legacy config, and reported autostart as off. Now it goes
// through resolveConfigPath (via resolveObserverConfig), the same
// canonical→legacy fallback every other bough command uses.
func TestResolveObserverConfig_LegacyConfigFallback(t *testing.T) {
	tmp := t.TempDir()
	legacy := filepath.Join(tmp, ".worktree-isolation.yaml")
	if err := os.WriteFile(legacy, []byte(`schema_version: 1
monorepo_root: "."
repositories:
  - {name: a, branch_strategy: develop}
registry: {path: .worktree-ports.json}
instinct:
  enabled: true
  observer:
    autostart: true
    interval_sec: 120
`), 0o644); err != nil {
		t.Fatalf("write legacy yaml: %v", err)
	}

	prev, _ := os.Getwd()
	defer func() { _ = os.Chdir(prev) }()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cfg, root, err := resolveObserverConfig(&cobra.Command{})
	if err != nil {
		t.Fatalf("resolveObserverConfig: %v", err)
	}
	if !cfg.Instinct.Observer.Autostart {
		t.Errorf("autostart = false, want true (legacy config was not found)")
	}
	if observerAutostartInterval(cfg) != 120 {
		t.Errorf("interval = %d, want 120", observerAutostartInterval(cfg))
	}
	if root == "" {
		t.Errorf("root should resolve to the monorepo root, got empty string")
	}
}
