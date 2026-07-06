//go:build darwin || linux

package compose

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSidecarState_WriteReadRoundTrip(t *testing.T) {
	worktreeRoot := t.TempDir()
	want := &upState{
		File:       "auba-api/compose.yml",
		Service:    "redis",
		Project:    "bough-f-feature-auba-api-compose-yml",
		TargetPort: 6379,
		HostPort:   56123,
		EnvPrefix:  "REDIS",
		ReadyProbe: "redis",
	}
	if err := writeSidecarState(worktreeRoot, want.HostPort, want); err != nil {
		t.Fatalf("writeSidecarState: %v", err)
	}
	got, err := readSidecarState(worktreeRoot, want.HostPort)
	if err != nil {
		t.Fatalf("readSidecarState: %v", err)
	}
	if *got != *want {
		t.Errorf("round-trip mismatch: got %+v, want %+v", *got, *want)
	}
}

func TestSidecarState_ReadMissingIsNotExist(t *testing.T) {
	worktreeRoot := t.TempDir()
	_, err := readSidecarState(worktreeRoot, 12345)
	if !os.IsNotExist(err) {
		t.Errorf("readSidecarState on a missing file: got err=%v, want os.IsNotExist", err)
	}
}

func TestSidecarState_PathIsPortScoped(t *testing.T) {
	worktreeRoot := t.TempDir()
	a := sidecarStatePath(worktreeRoot, 56123)
	b := sidecarStatePath(worktreeRoot, 56124)
	if a == b {
		t.Errorf("sidecar paths for different ports must differ: both = %q", a)
	}
	if filepath.Dir(a) != filepath.Join(worktreeRoot, ".local") {
		t.Errorf("sidecar state path = %q, want it under %q", a, filepath.Join(worktreeRoot, ".local"))
	}
}

func TestProvider_CacheState(t *testing.T) {
	p := New()
	if got := p.cachedState(1234); got != nil {
		t.Fatalf("cachedState on empty cache = %+v, want nil", got)
	}
	st := &upState{Service: "redis", EnvPrefix: "REDIS"}
	p.cacheState(1234, st)
	got := p.cachedState(1234)
	if got == nil || *got != *st {
		t.Errorf("cachedState(1234) = %+v, want %+v", got, st)
	}
	if got := p.cachedState(9999); got != nil {
		t.Errorf("cachedState for an unset port = %+v, want nil", got)
	}
}
