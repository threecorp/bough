package cli

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/registry"
)

// TestRunRemove_RefusesWhileAPortStillServes is the upgrade hazard: a
// worktree whose engine bough did not start (a Nix-backed one from v0.26.0
// or earlier) is still serving its datadir when remove runs. The plugin
// finds nothing of its own to stop, so remove must not delete anything.
func TestRunRemove_RefusesWhileAPortStillServes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	root := t.TempDir()
	wt := filepath.Join(root, "worktrees", "F-Live")
	data := filepath.Join(wt, ".local", "zzlive-data", "ibdata1")
	if err := os.MkdirAll(filepath.Dir(data), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(data, []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}
	regPath := filepath.Join(root, ".bough-ports.json")
	store := registry.NewStore(regPath, "")
	reg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry.Set(reg, "F-Live", "zzlive.main", port)
	if err := store.Save(reg, "seed"); err != nil {
		t.Fatal(err)
	}
	// No plugin exists for this kind, so Discover fails and Down never runs:
	// the guard must hold on the port alone.
	cfg := &config.Config{
		Engines:  []config.Engine{{Kind: "zzlive"}},
		Registry: config.RegistryConfig{Path: regPath},
		Teardown: config.TeardownConfig{RemoveDatadir: true},
	}

	start := time.Now()
	err = runRemove(context.Background(), io.Discard, cfg, root, "F-Live", wt, 0)
	if err == nil {
		t.Fatal("remove succeeded while the engine port still served")
	}
	if !strings.Contains(err.Error(), "no datadir, worktree or registry entry was deleted") {
		t.Errorf("error should say what was kept: %v", err)
	}
	if time.Since(start) < portReleaseWait {
		t.Errorf("refused after %v, before giving the port %v to close", time.Since(start), portReleaseWait)
	}
	if b, err := os.ReadFile(data); err != nil || string(b) != "live" {
		t.Errorf("datadir touched: %q, %v", b, err)
	}
	reg, _ = store.Load()
	if got, _ := registry.Get(reg, "F-Live", "zzlive.main"); got != port {
		t.Errorf("registry entry dropped: got %d want %d", got, port)
	}
}

// TestPortsStillServing_ReturnsAtOnceWhenClosed keeps the common path free:
// once the engine is stopped, remove must not wait out portReleaseWait.
func TestPortsStillServing_ReturnsAtOnceWhenClosed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	start := time.Now()
	if busy, err := portsStillServing(context.Background(), []int{port}, time.Minute); err != nil || len(busy) != 0 {
		t.Fatalf("closed port reported busy: %v, %v", busy, err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v on a closed port", d)
	}
}

// TestPortsStillServing_CancelIsNotAllClear: a Ctrl-C during the wait must
// stop remove, not let the next pass read every dial failure as "closed".
func TestPortsStillServing_CancelIsNotAllClear(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	busy, err := portsStillServing(ctx, []int{port}, time.Minute)
	if err == nil {
		t.Fatalf("cancelled check returned no error (busy=%v)", busy)
	}
}

// TestPortsStillServing_SeesIPv6OnlyListener: an engine bound to ::1 alone
// is still serving its datadir.
func TestPortsStillServing_SeesIPv6OnlyListener(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	busy, err := portsStillServing(context.Background(), []int{port}, 300*time.Millisecond)
	if err != nil || len(busy) != 1 {
		t.Fatalf("IPv6-only listener not seen: busy=%v err=%v", busy, err)
	}
}

// TestGuardedPorts covers which registry entries the guard checks: every
// engine port, one dropped from .bough.yaml included, but not the `ports:`
// section an app server may still hold.
func TestGuardedPorts(t *testing.T) {
	cfg := &config.Config{
		Engines: []config.Engine{{Kind: "mysql"}},
		Ports:   map[string]config.PortRange{"api": {Range: [2]int{45000, 45999}}},
	}
	entry := map[string]int{"mysql.main": 42001, "redis.main": 53001, "api.main": 45001}
	got := guardedPorts(entry, cfg)
	want := []int{42001, 53001}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("guardedPorts = %v, want %v", got, want)
	}
}
