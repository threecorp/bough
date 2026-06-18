//go:build darwin || linux

package redis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"
)

func TestProvider_PortRangeDefault(t *testing.T) {
	p := New()
	ranges, err := p.PortRangeDefault(context.Background())
	if err != nil {
		t.Fatalf("PortRangeDefault: %v", err)
	}
	mainRange, ok := ranges["main"]
	if !ok {
		t.Fatalf("PortRangeDefault did not declare role 'main' (got %v)", ranges)
	}
	if mainRange.Low != defaultPortLow || mainRange.High != defaultPortHigh {
		t.Errorf("defaults: got [%d, %d], want [%d, %d]", mainRange.Low, mainRange.High, defaultPortLow, defaultPortHigh)
	}
}

func TestProvider_PortRangeDefault_overrides(t *testing.T) {
	p := &Provider{PortLow: 60000, PortHigh: 61000}
	ranges, err := p.PortRangeDefault(context.Background())
	if err != nil {
		t.Fatalf("PortRangeDefault: %v", err)
	}
	mainRange := ranges["main"]
	if mainRange.Low != 60000 || mainRange.High != 61000 {
		t.Errorf("override: got [%d, %d], want [60000, 61000]", mainRange.Low, mainRange.High)
	}
}

func TestProvider_EnvVars(t *testing.T) {
	p := New()
	out, err := p.EnvVars(context.Background(), &api.EnvVarsReq{
		Ports: []api.PortSpec{{Role: "main", Port: 53345}},
	})
	if err != nil {
		t.Fatalf("EnvVars: %v", err)
	}
	cases := map[string]string{
		"BOUGH_REDIS_HOST": "127.0.0.1",
		"BOUGH_REDIS_PORT": "53345",
		"BOUGH_REDIS_URL":  "redis://127.0.0.1:53345/0",
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
		`BOUGH_REDIS_PORT`,
		`BOUGH_REDIS_DATADIR`,
		`pkgs.redis`,
		`bind = "127.0.0.1"`,
	}
	for _, c := range checks {
		if !strings.Contains(contents, c) {
			t.Errorf("flake.nix missing expected fragment: %q", c)
		}
	}

	lockPath := filepath.Join(dst, "flake.lock")
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("flake.lock not extracted: %v", err)
	}
	lockRaw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read flake.lock: %v", err)
	}
	if !strings.Contains(string(lockRaw), `"nixpkgs"`) {
		t.Errorf("flake.lock missing nixpkgs input node")
	}
}

func TestProvider_Cleanup(t *testing.T) {
	tmp := t.TempDir()
	datadir := filepath.Join(tmp, "redis-data")
	if err := os.MkdirAll(datadir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(datadir, "stub"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := New().Cleanup(context.Background(), datadir, nil); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(datadir); !os.IsNotExist(err) {
		t.Errorf("datadir should be gone, stat err=%v", err)
	}
}

func TestProvider_Cleanup_emptyDatadir(t *testing.T) {
	if err := New().Cleanup(context.Background(), "", nil); err == nil {
		t.Fatalf("expected error on empty datadir, got nil")
	}
}
