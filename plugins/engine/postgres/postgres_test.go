//go:build darwin || linux

package postgres

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"

	"github.com/ikeikeikeike/bough/pkg/procutil"
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
		Ports:            []api.PortSpec{{Role: "main", Port: 50345}},
		SocketDir:        "/tmp",
		InitialResources: []api.ResourceSpec{{Type: "database", Name: "bough"}},
	})
	if err != nil {
		t.Fatalf("EnvVars: %v", err)
	}
	cases := map[string]string{
		"BOUGH_POSTGRES_HOST":       "127.0.0.1",
		"BOUGH_POSTGRES_PORT":       "50345",
		"BOUGH_POSTGRES_SOCKET_DIR": "/tmp",
	}
	for k, want := range cases {
		if got := out[k]; got != want {
			t.Errorf("%s: got %q want %q", k, got, want)
		}
	}
}

func TestProvider_EnvVars_socketDirDefault(t *testing.T) {
	p := New()
	out, err := p.EnvVars(context.Background(), &api.EnvVarsReq{
		Ports: []api.PortSpec{{Role: "main", Port: 12345}},
	})
	if err != nil {
		t.Fatalf("EnvVars: %v", err)
	}
	if out["BOUGH_POSTGRES_SOCKET_DIR"] != "/tmp" {
		t.Errorf("SocketDir default: got %q, want /tmp", out["BOUGH_POSTGRES_SOCKET_DIR"])
	}
}

// TestProvider_Up_NixRejectsVersionOutsidePinnedLine guards the nix half
// of engines[].version: the flake pins pkgs.postgresql_16, so
// `version: "17"` used to be accepted and silently start 16. It must
// refuse before anything is written — no flake dir, no startup log.
func TestProvider_Up_NixRejectsVersionOutsidePinnedLine(t *testing.T) {
	tmp := t.TempDir()
	p := New()
	err := p.Up(context.Background(), &api.UpReq{
		WorktreeRoot: tmp,
		Ports:        []api.PortSpec{{Role: "main", Port: 50432}},
		Extras:       map[string]string{"version": "17"},
	})
	if err == nil {
		t.Fatal("Up with version 17 on the nix backend = nil error, want an error")
	}
	for _, want := range []string{nixPinnedVersion, "backend: docker"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Up error = %q, want it to mention %q", err, want)
		}
	}
	flakeDir := filepath.Join(tmp, flakeDirRelative)
	if _, statErr := os.Stat(flakeDir); statErr == nil {
		t.Errorf("Up deployed the flake to %s despite rejecting the version", flakeDir)
	}
}

func TestDeployFlake_extractsEmbeddedAssets(t *testing.T) {
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "extracted")
	if err := procutil.DeployFlake(nixAssets, "nix", dst); err != nil {
		t.Fatalf("DeployFlake: %v", err)
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
		`BOUGH_POSTGRES_PORT`,
		`BOUGH_POSTGRES_SOCKET_DIR`,
		`BOUGH_POSTGRES_DATADIR`,
		nixPackageAttr,
		`listen_addresses`,
		`socketDir`,
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
	datadir := filepath.Join(tmp, "postgres-data")
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
