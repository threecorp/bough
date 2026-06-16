//go:build darwin || linux

package mysql

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
	if low != 42000 || high != 44999 {
		t.Errorf("defaults: got [%d, %d], want [42000, 44999]", low, high)
	}
}

func TestProvider_PortRangeDefault_overrides(t *testing.T) {
	p := &Provider{PortLow: 50000, PortHigh: 51000}
	low, high, err := p.PortRangeDefault(context.Background())
	if err != nil {
		t.Fatalf("PortRangeDefault: %v", err)
	}
	if low != 50000 || high != 51000 {
		t.Errorf("override: got [%d, %d], want [50000, 51000]", low, high)
	}
}

func TestProvider_EnvVars(t *testing.T) {
	p := New()
	out, err := p.EnvVars(context.Background(), api.EnvVarsReq{
		Port: 42345, SocketDir: "/tmp", InitialDatabases: []string{"bough"},
	})
	if err != nil {
		t.Fatalf("EnvVars: %v", err)
	}
	cases := map[string]string{
		"BOUGH_MYSQL_HOST":   "127.0.0.1",
		"BOUGH_MYSQL_PORT":   "42345",
		"BOUGH_MYSQL_SOCKET": "/tmp/bough-mysql-42345.sock",
	}
	for k, want := range cases {
		if got := out[k]; got != want {
			t.Errorf("%s: got %q want %q", k, got, want)
		}
	}
}

func TestProvider_EnvVars_socketDirDefault(t *testing.T) {
	p := New()
	out, err := p.EnvVars(context.Background(), api.EnvVarsReq{Port: 12345})
	if err != nil {
		t.Fatalf("EnvVars: %v", err)
	}
	if !strings.HasPrefix(out["BOUGH_MYSQL_SOCKET"], "/tmp/") {
		t.Errorf("SocketDir default: got %q, want /tmp/ prefix", out["BOUGH_MYSQL_SOCKET"])
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
		`BOUGH_MYSQL_PORT`,
		`BOUGH_MYSQL_SOCKET_DIR`,
		`BOUGH_MYSQL_DATADIR`,
		`pkgs.mysql84`,
		`mysqlx = "OFF"`,
		`lib.mkForce`, // readiness probe override
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
	datadir := filepath.Join(tmp, "mysql-data")
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
