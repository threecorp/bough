package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/config"
	"github.com/ikeikeikeike/bough/internal/registry"
)

// Separate processes catch lost read/allocate/save updates that a process-local mutex cannot prevent.
func TestAllocateAllPorts_ConcurrentProcesses(t *testing.T) {
	if root := os.Getenv("BOUGH_TEST_ALLOCATION_ROOT"); root != "" {
		if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
			t.Fatal(err)
		}
		name := os.Getenv("BOUGH_TEST_ALLOCATION_NAME")
		cfg := &config.Config{Registry: config.RegistryConfig{Path: ".bough/ports.json"}, Ports: map[string]config.PortRange{"api": {Range: [2]int{36000, 38999}}}}
		_, ports, err := allocateAllPorts(cfg, root, name)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(ports)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name+".json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, historicalCount := range []int{0, 5000} {
		t.Run(fmt.Sprint(historicalCount), func(t *testing.T) {
			root := t.TempDir()
			store := registry.NewStore(filepath.Join(root, ".bough/ports.json"), "")
			reg := registry.Registry{}
			for i := 0; i < historicalCount; i++ {
				reg[fmt.Sprintf("historical-%d", i)] = map[string]int{"unused.main": 10000}
			}
			if err := store.Save(reg, "fixture"); err != nil {
				t.Fatal(err)
			}
			// These names independently share the original crc32 modulo 3000 offset.
			names := []string{"F-Collision-1325", "F-Collision-1993", "F-Collision-8713", "F-Collision-9163", "F-Collision-12847", "F-Collision-15237", "F-Collision-18807", "F-Collision-20951"}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			type child struct {
				cmd   *exec.Cmd
				start io.WriteCloser
				log   bytes.Buffer
			}
			children := make([]child, len(names))
			for i, name := range names {
				c := &children[i]
				c.cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAllocateAllPorts_ConcurrentProcesses$")
				c.cmd.Env = append(os.Environ(), "BOUGH_TEST_ALLOCATION_ROOT="+root, "BOUGH_TEST_ALLOCATION_NAME="+name)
				c.cmd.Stdout, c.cmd.Stderr = &c.log, &c.log
				var err error
				c.start, err = c.cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := c.cmd.Start(); err != nil {
					t.Fatal(err)
				}
			}
			for i := range children {
				if _, err := children[i].start.Write([]byte{1}); err != nil {
					t.Error(err)
				}
				_ = children[i].start.Close()
			}
			for i := range children {
				if err := children[i].cmd.Wait(); err != nil {
					t.Errorf("allocation %s: %v\n%s", names[i], err, children[i].log.String())
				}
			}
			actual, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(actual) != historicalCount+8 {
				t.Errorf("reservation count = %d, want %d", len(actual), historicalCount+8)
			}
			owners := map[int]string{}
			for _, name := range names {
				raw, err := os.ReadFile(filepath.Join(root, name+".json"))
				if err != nil {
					t.Error(err)
					continue
				}
				var ports map[string]int
				if err := json.Unmarshal(raw, &ports); err != nil {
					t.Fatal(err)
				}
				port := ports["api"]
				if prior, exists := owners[port]; exists {
					t.Errorf("duplicate port %d assigned to %s and %s", port, prior, name)
				}
				owners[port] = name
				if saved, ok := registry.Get(actual, name, "api.main"); !ok || saved != port {
					t.Errorf("reservation %s = (%d,%v), returned port %d", name, saved, ok, port)
				}
			}
		})
	}
}

func TestRunPostCreateHooks_MigrationFailureSkipsSeed(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"demo", "independent"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Repositories: []config.Repository{
		{Name: "demo", PostCreate: []string{"exit 17", "touch seed-ran"}},
		{Name: "independent", PostCreate: []string{"touch setup-ran"}},
	}}
	failed := runPostCreateHooks(context.Background(), io.Discard, cfg, root, nil)
	if len(failed) != 1 {
		t.Errorf("failure count = %d, want 1", len(failed))
	}
	if _, err := os.Stat(filepath.Join(root, "demo/seed-ran")); !os.IsNotExist(err) {
		t.Errorf("seed ran after failed migration: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "independent/setup-ran")); err != nil {
		t.Errorf("independent repository setup skipped: %v", err)
	}
}

func TestCreate_MigrationFailurePropagates(t *testing.T) {
	for _, mode := range []string{"strict-cli", "official-hook", "legacy-hook", "default-cli"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			repo := filepath.Join(root, "demo")
			if err := os.MkdirAll(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "test fixture"}} {
				c := exec.Command("git", args...)
				c.Dir = repo
				if out, err := c.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			yaml := "schema_version: 2\nmonorepo_root: .\nrepositories:\n  - name: demo\n    branch_strategy: main\n    post_create:\n      - 'test ! -f ../../migration-fails'\n      - 'touch seed-ran'\nregistry:\n  path: .bough/ports.json\n"
			if err := os.WriteFile(filepath.Join(root, ".bough.yaml"), []byte(yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			// The marker is outside the worktree so a retry uses the same materialised repository.
			failureMarker := filepath.Join(root, "worktrees/migration-fails")
			if err := os.MkdirAll(filepath.Dir(failureMarker), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(failureMarker, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			execute := func() error {
				var out, stderr bytes.Buffer
				cmd := newCreateCmd()
				args := []string{"--name", "F-Migrate", "--cwd", root, "--no-fetch"}
				switch mode {
				case "official-hook":
					cmd = newHookHandleCmd()
					args = []string{"--event", "WorktreeCreate"}
					cmd.SetIn(bytes.NewBufferString(fmt.Sprintf(`{"name":"F-Migrate","cwd":%q}`, root)))
				case "strict-cli":
					args = append(args, "--strict")
				case "legacy-hook":
					// Hooks installed before `bough hook handle` ran create
					// with the payload on stdin; that path must fail too.
					args = []string{"--stdin-json", "--no-fetch"}
					cmd.SetIn(bytes.NewBufferString(fmt.Sprintf(`{"name":"F-Migrate","cwd":%q}`, root)))
				}
				cmd.SetArgs(args)
				cmd.SetOut(&out)
				cmd.SetErr(&stderr)
				return cmd.Execute()
			}
			err := execute()
			if mode == "default-cli" && err != nil {
				t.Errorf("default CLI compatibility: %v", err)
			} else if mode != "default-cli" && err == nil {
				t.Error("setup failure reported as success")
			}
			seed := filepath.Join(root, "worktrees/F-Migrate/demo/seed-ran")
			if _, err := os.Stat(seed); !os.IsNotExist(err) {
				t.Errorf("seed ran after failed migration: %v", err)
			}
			if err := os.Remove(failureMarker); err != nil {
				t.Fatal(err)
			}
			if err := execute(); err != nil {
				t.Fatalf("retry after migration repaired: %v", err)
			}
			if _, err := os.Stat(seed); err != nil {
				t.Errorf("successful retry did not seed: %v", err)
			}
		})
	}
}

func TestRunRemove_PreservesReservationCreatedDuringTeardown(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktrees/old")
	if err := os.MkdirAll(filepath.Join(worktree, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Registry: config.RegistryConfig{Path: ".bough/ports.json"},
		Ports:    map[string]config.PortRange{"api": {Range: [2]int{36000, 38999}}},
		Repositories: []config.Repository{{Name: "demo", PreRemove: []string{
			"touch teardown-ready; while [ ! -f teardown-continue ]; do sleep 0.01; done",
		}}},
	}
	store := registry.NewStore(filepath.Join(root, ".bough/ports.json"), "")
	if err := store.Save(registry.Registry{"old": {"api.main": 36000}}, "fixture"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runRemove(ctx, io.Discard, cfg, root, "old", worktree, 0) }()
	ready := filepath.Join(worktree, "demo/teardown-ready")
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("teardown hook did not reach barrier")
		case err := <-done:
			t.Fatalf("remove stopped before barrier: %v", err)
		case <-time.After(time.Millisecond):
		}
	}
	_, ports, allocationErr := allocateAllPorts(cfg, root, "new")
	if err := os.WriteFile(filepath.Join(worktree, "demo/teardown-continue"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if allocationErr != nil {
		t.Fatal(allocationErr)
	}
	actual, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := actual["old"]; exists {
		t.Error("removed reservation still present")
	}
	if p, ok := registry.Get(actual, "new", "api.main"); !ok || p != ports["api"] {
		t.Errorf("concurrent reservation lost: (%d,%v), allocated %d", p, ok, ports["api"])
	}
}

// TestAllocateAllPorts_OverlappingRangesGetDistinctPorts: the allocator
// starts every kind at the same hash offset of the worktree name, so two
// kinds sharing a range were handed the same port on every create.
func TestAllocateAllPorts_OverlappingRangesGetDistinctPorts(t *testing.T) {
	root := t.TempDir()
	shared := [2]int{40000, 40099}
	cfg := &config.Config{
		Registry: config.RegistryConfig{Path: ".bough/ports.json"},
		Engines: []config.Engine{
			{Kind: "mysql", PortRanges: map[string][2]int{"main": shared}},
			{Kind: "redis", PortRanges: map[string][2]int{"main": shared}},
		},
		Ports: map[string]config.PortRange{"api": {Range: shared}},
	}
	for _, name := range []string{"F-A", "F-B"} {
		enginePorts, nonEnginePorts, err := allocateAllPorts(cfg, root, name)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[int]string{}
		for kind, port := range map[string]int{"mysql": enginePorts["mysql"], "redis": enginePorts["redis"], "api": nonEnginePorts["api"]} {
			if other, dup := seen[port]; dup {
				t.Errorf("%s: %s and %s were both given port %d", name, kind, other, port)
			}
			seen[port] = kind
		}
	}
}

// TestAllocateAllPorts_OverlappingPortsKindsAreReproducible: ports: is a
// map, and with every kind avoiding every held port the kind allocated
// first took the shared candidate, so a re-create could swap api and web.
func TestAllocateAllPorts_OverlappingPortsKindsAreReproducible(t *testing.T) {
	shared := config.PortRange{Range: [2]int{40000, 40099}}
	cfg := &config.Config{
		Registry: config.RegistryConfig{Path: ".bough/ports.json"},
		Ports:    map[string]config.PortRange{"api": shared, "web": shared, "admin": shared, "docs": shared},
	}
	var first map[string]int
	for i := 0; i < 20; i++ {
		_, ports, err := allocateAllPorts(cfg, t.TempDir(), "F-A")
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = ports
			continue
		}
		for kind, port := range ports {
			if first[kind] != port {
				t.Fatalf("run %d gave %s port %d, first run gave %d", i, kind, port, first[kind])
			}
		}
	}
}
