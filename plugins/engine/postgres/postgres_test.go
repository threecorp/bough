//go:build darwin || linux

package postgres

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
		Ports:            []api.PortSpec{{Role: "main", Port: 50345}},
		InitialResources: []api.ResourceSpec{{Type: "database", Name: "bough"}},
	})
	if err != nil {
		t.Fatalf("EnvVars: %v", err)
	}
	cases := map[string]string{
		"BOUGH_POSTGRES_HOST": "127.0.0.1",
		"BOUGH_POSTGRES_PORT": "50345",
	}
	for k, want := range cases {
		if got := out[k]; got != want {
			t.Errorf("%s: got %q want %q", k, got, want)
		}
	}
	// The container publishes TCP only; a socket-dir key here would name
	// a directory nothing binds into.
	if len(out) != len(cases) {
		t.Errorf("EnvVars returned %d keys %v, want exactly %v", len(out), out, cases)
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
		Ports:   []api.PortSpec{{Role: "main", Port: 50432}},
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
	for _, want := range []string{"postgres", `"nix"`, api.DefaultBackend} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestPgdataPin pins which images get PGDATA overridden: only those whose
// own PGDATA would put the data outside the bind mount.
func TestPgdataPin(t *testing.T) {
	cases := map[string]struct {
		env  []string
		want string
	}{
		"official 17 keeps its default":       {[]string{"PGDATA=/var/lib/postgresql/data"}, ""},
		"official 18 is moved into the mount": {[]string{"PGDATA=/var/lib/postgresql/18/docker"}, "PGDATA=/var/lib/postgresql/data"},
		"custom subdir of the mount is kept":  {[]string{"PGDATA=/var/lib/postgresql/data/pgdata"}, ""},
		"no PGDATA at all gets the mount":     {[]string{"PATH=/usr/bin"}, "PGDATA=/var/lib/postgresql/data"},
		"a sibling prefix is not the mount":   {[]string{"PGDATA=/var/lib/postgresql/data2"}, "PGDATA=/var/lib/postgresql/data"},
		"a dot-dot escape is not the mount":   {[]string{"PGDATA=/var/lib/postgresql/data/../outside"}, "PGDATA=/var/lib/postgresql/data"},
	}
	for name, tc := range cases {
		if got := pgdataPin(tc.env); got != tc.want {
			t.Errorf("%s: pgdataPin(%v) = %q, want %q", name, tc.env, got, tc.want)
		}
	}
}
