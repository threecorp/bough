//go:build darwin || linux

package elasticsearch

import (
	"context"
	"errors"
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

// TestDockerBackend_Up_InvalidHeapRejectedBeforeAnySideEffect keeps the
// property the earlier host-process backend was tested for: a bad heap
// is refused while nothing has been created. It is observable because
// the datadir Up would otherwise mkdir must not exist afterwards, and
// it runs without a Docker daemon because every pure check precedes the
// first client call.
func TestDockerBackend_Up_InvalidHeapRejectedBeforeAnySideEffect(t *testing.T) {
	datadir := filepath.Join(t.TempDir(), "elasticsearch-data")

	err := dockerBackend{}.Up(context.Background(), &api.UpReq{
		Ports:   []api.PortSpec{{Role: "main", Port: 59200}},
		Datadir: datadir,
		Extras:  map[string]string{"version": "9.5.3", "es.heap": "1g; rm -rf /"},
	})
	if err == nil {
		t.Fatal("Up with an invalid heap = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "invalid heap") {
		t.Errorf("Up error = %q, want it to mention invalid heap", err)
	}
	if _, statErr := os.Stat(datadir); !os.IsNotExist(statErr) {
		t.Errorf("Up created %s despite rejecting the heap; validation must precede any side effect", datadir)
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

// TestNew_RegistersDocker pins what a bare `bough create` runs: the
// constructor seeds exactly one backend, under the token the host sends
// when .bough.yaml omits `backend:`.
func TestNew_RegistersDocker(t *testing.T) {
	backends := New().Backends
	if len(backends) != 1 {
		t.Fatalf("New() registered %d backends %v, want exactly one", len(backends), backends)
	}
	if _, ok := backends[api.DefaultBackend].(dockerBackend); !ok {
		t.Errorf("New() registered %T under %q, want dockerBackend", backends[api.DefaultBackend], api.DefaultBackend)
	}
}

// TestProvider_Up_UnknownBackendIsRefused is the guard for a .bough.yaml
// left over from the nix era: the token must be refused by name before
// any daemon call, not silently run on whatever is registered.
func TestProvider_Up_UnknownBackendIsRefused(t *testing.T) {
	err := New().Up(context.Background(), &api.UpReq{
		Ports:   []api.PortSpec{{Role: "main", Port: 59200}},
		Datadir: t.TempDir(),
		Extras:  map[string]string{"backend": "nix"},
	})
	if err == nil {
		t.Fatal("Up accepted backend: nix")
	}
	var unknown *api.UnknownBackendError
	if !errors.As(err, &unknown) {
		t.Fatalf("Up error = %T (%v), want *api.UnknownBackendError", err, err)
	}
	for _, want := range []string{"elasticsearch", `"nix"`, api.DefaultBackend} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
