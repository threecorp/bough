//go:build darwin || linux

package elasticsearch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/ikeikeikeike/bough/plugins/db/api"
)

func TestProvider_PortRangeDefault(t *testing.T) {
	p := New()
	low, high, err := p.PortRangeDefault(context.Background())
	if err != nil {
		t.Fatalf("PortRangeDefault: %v", err)
	}
	if low != defaultPortLow || high != defaultPortHigh {
		t.Errorf("defaults: got [%d, %d], want [%d, %d]", low, high, defaultPortLow, defaultPortHigh)
	}
}

func TestProvider_PortRangeDefault_overrides(t *testing.T) {
	p := &Provider{PortLow: 60000, PortHigh: 61000}
	low, high, err := p.PortRangeDefault(context.Background())
	if err != nil {
		t.Fatalf("PortRangeDefault: %v", err)
	}
	if low != 60000 || high != 61000 {
		t.Errorf("override: got [%d, %d], want [60000, 61000]", low, high)
	}
}

func TestProvider_EnvVars(t *testing.T) {
	p := New()
	out, err := p.EnvVars(context.Background(), api.EnvVarsReq{Port: 56345})
	if err != nil {
		t.Fatalf("EnvVars: %v", err)
	}
	cases := map[string]string{
		"BOUGH_ELASTICSEARCH_HOST": "127.0.0.1",
		"BOUGH_ELASTICSEARCH_PORT": "56345",
		"BOUGH_ELASTICSEARCH_URL":  "http://127.0.0.1:56345",
	}
	for k, want := range cases {
		if got := out[k]; got != want {
			t.Errorf("%s: got %q want %q", k, got, want)
		}
	}
}

func TestDeployFlake_extractsEmbeddedAssets(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "extracted")
	if err := deployFlake(dst); err != nil {
		t.Fatalf("deployFlake: %v", err)
	}
	flakePath := filepath.Join(dst, "flake.nix")
	if _, err := os.Stat(flakePath); err != nil {
		t.Fatalf("flake.nix not extracted: %v", err)
	}
	raw, err := os.ReadFile(flakePath)
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	contents := string(raw)
	checks := []string{
		`services-flake.url`,
		`process-compose-flake.url`,
		`BOUGH_ELASTICSEARCH_PORT`,
		`BOUGH_ELASTICSEARCH_DATADIR`,
		`BOUGH_ELASTICSEARCH_HEAP`,
		`pkgs.elasticsearch7`,
		`discovery.type=single-node`,
		`xpack.security.enabled=false`,
		`_cluster/health`,
	}
	for _, c := range checks {
		if !strings.Contains(contents, c) {
			t.Errorf("flake.nix missing expected fragment: %q", c)
		}
	}
}

func TestProvider_Cleanup(t *testing.T) {
	tmp := t.TempDir()
	datadir := filepath.Join(tmp, "elasticsearch-data")
	if err := os.MkdirAll(datadir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(datadir, "stub"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := New().Cleanup(context.Background(), datadir, 0); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(datadir); !os.IsNotExist(err) {
		t.Errorf("datadir should be gone, stat err=%v", err)
	}
}

func TestProvider_Cleanup_emptyDatadir(t *testing.T) {
	if err := New().Cleanup(context.Background(), "", 0); err == nil {
		t.Fatalf("expected error on empty datadir, got nil")
	}
}
