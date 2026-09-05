//go:build darwin || linux

package elasticsearch

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
		Ports: []api.PortSpec{{Role: "main", Port: 56345}},
	})
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

func TestHeapSizePattern(t *testing.T) {
	valid := []string{"1g", "512m", "2048k", "1G", "256M", "1024", "10g"}
	for _, v := range valid {
		if !heapSizePattern.MatchString(v) {
			t.Errorf("heapSizePattern rejected a valid heap %q", v)
		}
	}
	// Reject anything a stray extras.heap could smuggle into the
	// ES_JAVA_OPTS="-Xms${heap}..." shell interpolation (issue #82 item 6).
	invalid := []string{"", "1gb", "1.5g", "-1g", "abc", "1 g", `1g"`, "$(whoami)", "1g; rm -rf /"}
	for _, v := range invalid {
		if heapSizePattern.MatchString(v) {
			t.Errorf("heapSizePattern accepted an invalid/injectable heap %q", v)
		}
	}
}

func TestValidateHeap(t *testing.T) {
	valid := []string{"1g", "512m", "2048k", "1G", "256M", "1024", "10g"}
	for _, v := range valid {
		if err := validateHeap(v); err != nil {
			t.Errorf("validateHeap(%q) = %v, want nil", v, err)
		}
	}
	invalid := []string{"", "1gb", "1.5g", "-1g", "abc", "1 g", `1g"`, "$(whoami)", "1g; rm -rf /"}
	for _, v := range invalid {
		if err := validateHeap(v); err == nil {
			t.Errorf("validateHeap(%q) = nil, want an error", v)
		}
	}
}

// TestProvider_Up_InvalidHeapRejectedBeforeLogFileOpen is the
// regression guard for the fd-leak found by reviewing this PR: the
// heap-format check used to run AFTER opening the startup log file, so
// the early-return on an invalid heap leaked that file handle (the
// sibling cmd.Start() failure path a few lines later does close it).
// Validating the heap before any file is opened both fixes the leak
// and is directly observable: the startup log must not exist at all
// after Up() rejects a bad heap.
func TestProvider_Up_InvalidHeapRejectedBeforeLogFileOpen(t *testing.T) {
	tmp := t.TempDir()
	p := New()
	err := p.Up(context.Background(), &api.UpReq{
		WorktreeRoot: tmp,
		Ports:        []api.PortSpec{{Role: "main", Port: 59200}},
		Extras:       map[string]string{"heap": "1g; rm -rf /"},
	})
	if err == nil {
		t.Fatal("Up with an invalid heap = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "invalid heap") {
		t.Errorf("Up error = %q, want it to mention invalid heap", err)
	}
	logPath := filepath.Join(tmp, startupLogRelative)
	if _, statErr := os.Stat(logPath); statErr == nil {
		t.Errorf("Up with an invalid heap created the startup log at %s — heap validation must run before opening it", logPath)
	}
}

// TestProvider_Up_NixRejectsVersionOutsidePinnedLine guards the other
// half of engines[].version: nixpkgs carries no Elasticsearch 8 or 9, so
// a 9.x version used to be accepted here and silently start the flake's
// 7 line. Like the heap check above, it must refuse before anything is
// written — no flake dir, no startup log.
func TestProvider_Up_NixRejectsVersionOutsidePinnedLine(t *testing.T) {
	tmp := t.TempDir()
	p := New()
	err := p.Up(context.Background(), &api.UpReq{
		WorktreeRoot: tmp,
		Ports:        []api.PortSpec{{Role: "main", Port: 59200}},
		Extras:       map[string]string{"version": "9.4.1"},
	})
	if err == nil {
		t.Fatal("Up with version 9.4.1 on the nix backend = nil error, want an error")
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
		`BOUGH_ELASTICSEARCH_PORT`,
		`BOUGH_ELASTICSEARCH_DATADIR`,
		`BOUGH_ELASTICSEARCH_HEAP`,
		nixPackageAttr,
		`discovery.type=single-node`,
		`xpack.security.enabled=false`,
		`_cluster/health`,
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
	datadir := filepath.Join(tmp, "elasticsearch-data")
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
