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
	defer ln.Close()
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
	if !strings.Contains(err.Error(), "nothing was deleted") {
		t.Errorf("error should say nothing was deleted: %v", err)
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
	if busy := portsStillServing(context.Background(), []int{port}, time.Minute); len(busy) != 0 {
		t.Fatalf("closed port reported busy: %v", busy)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v on a closed port", d)
	}
}
