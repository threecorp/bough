//go:build darwin || linux

package compose

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/ikeikeikeike/bough/plugins/engine/api"
)

func TestProvider_EnvVars_KnownServiceGetsURL(t *testing.T) {
	p := New()
	p.cacheState(56123, &upState{Service: "redis", EnvPrefix: "REDIS"})
	out, err := p.EnvVars(context.Background(), &api.EnvVarsReq{
		Ports: []api.PortSpec{{Role: "main", Port: 56123}},
	})
	if err != nil {
		t.Fatalf("EnvVars: %v", err)
	}
	want := map[string]string{
		"BOUGH_REDIS_HOST": "127.0.0.1",
		"BOUGH_REDIS_PORT": "56123",
		"BOUGH_REDIS_URL":  "redis://127.0.0.1:56123",
	}
	for k, v := range want {
		if got := out[k]; got != v {
			t.Errorf("%s: got %q want %q", k, got, v)
		}
	}
}

func TestProvider_EnvVars_UnknownServiceSkipsURL(t *testing.T) {
	p := New()
	p.cacheState(56200, &upState{Service: "some-custom-thing", EnvPrefix: "CUSTOM"})
	out, err := p.EnvVars(context.Background(), &api.EnvVarsReq{
		Ports: []api.PortSpec{{Role: "main", Port: 56200}},
	})
	if err != nil {
		t.Fatalf("EnvVars: %v", err)
	}
	if got := out["BOUGH_CUSTOM_HOST"]; got != "127.0.0.1" {
		t.Errorf("BOUGH_CUSTOM_HOST: got %q want 127.0.0.1", got)
	}
	if _, ok := out["BOUGH_CUSTOM_URL"]; ok {
		t.Errorf("BOUGH_CUSTOM_URL should be absent for an unrecognized service, got %q", out["BOUGH_CUSTOM_URL"])
	}
}

func TestProvider_EnvVars_NoCacheFallsBackToGenericPrefix(t *testing.T) {
	p := New()
	out, err := p.EnvVars(context.Background(), &api.EnvVarsReq{
		Ports: []api.PortSpec{{Role: "main", Port: 56300}},
	})
	if err != nil {
		t.Fatalf("EnvVars: %v", err)
	}
	if got := out["BOUGH_COMPOSE_PORT"]; got != "56300" {
		t.Errorf("BOUGH_COMPOSE_PORT: got %q want 56300 (fallback prefix when no cached state exists)", got)
	}
}

func TestProvider_Cleanup_IsNoOp(t *testing.T) {
	p := New()
	dir := t.TempDir()
	marker := filepath.Join(dir, "should-survive")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := p.Cleanup(context.Background(), dir, []int{56123}); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("Cleanup must be a no-op but the seeded file is gone: %v", err)
	}
}

func TestParseBoundPort(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"ipv4 host:port", "0.0.0.0:56123\n", 56123},
		{"bare host:port no newline", "127.0.0.1:6379", 6379},
		{"unparsable", "not-a-port-line", 0},
		{"empty", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseBoundPort(tc.in); got != tc.want {
				t.Errorf("parseBoundPort(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestProvider_Up_RejectsMissingExtras(t *testing.T) {
	p := New()
	worktreeRoot := t.TempDir()
	repoDir := filepath.Join(worktreeRoot, "demo-api")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	err := p.Up(context.Background(), &api.UpReq{
		WorktreeRoot: repoDir,
		Ports:        []api.PortSpec{{Role: "main", Port: 56123}},
		Extras:       map[string]string{}, // compose.file/service/target_port all missing
	})
	if err == nil {
		t.Fatal("Up with no compose.* extras = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "compose.file") {
		t.Errorf("error %q should mention the missing extras", err.Error())
	}
}

func TestProvider_Up_RejectsMissingComposeFile(t *testing.T) {
	p := New()
	worktreeRoot := t.TempDir()
	repoDir := filepath.Join(worktreeRoot, "demo-api")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	err := p.Up(context.Background(), &api.UpReq{
		WorktreeRoot: repoDir,
		Ports:        []api.PortSpec{{Role: "main", Port: 56123}},
		Extras: map[string]string{
			"compose.file":        "demo-api/does-not-exist.yml",
			"compose.service":     "redis",
			"compose.target_port": "6379",
		},
	})
	if err == nil {
		t.Fatal("Up with a nonexistent compose file = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "does-not-exist.yml") {
		t.Errorf("error %q should name the missing file", err.Error())
	}
}

// TestProvider_Up_ReusesAlreadyRunningContainer is the regression
// guard for the conformance suite's UpReuse phase (CONTRACT.md clause
// 3): calling Up a second time while the service is ALREADY running
// (no Down in between) must be a no-op returning nil, not an error.
// This brings up an actual redis:7-alpine container, matching the
// same real-docker assumption TestProvider_Up_RejectsPortAlreadyInUse
// already makes in this file.
func TestProvider_Up_ReusesAlreadyRunningContainer(t *testing.T) {
	p := New()
	worktreeRoot := t.TempDir()
	repoDir := filepath.Join(worktreeRoot, "demo-api")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "compose.yml"), []byte("services: {redis: {image: redis:7-alpine}}"), 0o644); err != nil {
		t.Fatalf("seed compose.yml: %v", err)
	}
	const port = 59201
	req := &api.UpReq{
		WorktreeRoot: repoDir,
		Ports:        []api.PortSpec{{Role: "main", Port: port}},
		Extras: map[string]string{
			"compose.file":        "demo-api/compose.yml",
			"compose.service":     "redis",
			"compose.target_port": "6379",
		},
	}
	ctx := context.Background()
	if err := p.Up(ctx, req); err != nil {
		t.Fatalf("first Up: %v", err)
	}
	t.Cleanup(func() {
		_ = p.Down(ctx, &api.DownReq{Ports: []int{port}, WorktreeRoot: repoDir, GracefulTimeoutSec: 10})
	})

	if err := p.Up(ctx, req); err != nil {
		t.Errorf("second Up on an already-running container must be a no-op returning nil (up-or-reuse); got: %v", err)
	}

	ok, err := p.ReadyCheck(ctx, []int{port}, 10)
	if err != nil || !ok {
		t.Errorf("service must still be reachable after reuse: ok=%v err=%v", ok, err)
	}
}

// TestProvider_Up_RejectsPortAlreadyInUse is the regression guard for
// the Fault_PortConflict finding: docker compose up's own bind
// failure is not reliably surfaced on every Docker backend (Docker
// Desktop's macOS proxy layer can silently paper over a host-side
// conflict that a native Linux daemon would reject) — Up() must
// proactively check port availability itself, the same way the
// mysql/postgres/redis/elasticsearch docker.go plugins already do via
// dockerutil.IsPortFree, rather than trusting `docker compose up`'s
// exit code alone.
func TestProvider_Up_RejectsPortAlreadyInUse(t *testing.T) {
	p := New()
	worktreeRoot := t.TempDir()
	repoDir := filepath.Join(worktreeRoot, "demo-api")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "compose.yml"), []byte("services: {redis: {}}"), 0o644); err != nil {
		t.Fatalf("seed compose.yml: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	err = p.Up(context.Background(), &api.UpReq{
		WorktreeRoot: repoDir,
		Ports:        []api.PortSpec{{Role: "main", Port: port}},
		Extras: map[string]string{
			"compose.file":        "demo-api/compose.yml",
			"compose.service":     "redis",
			"compose.target_port": "6379",
		},
	})
	if err == nil {
		t.Fatal("Up on a port already held by a sidecar listener = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Errorf("error %q should mention the port conflict", err.Error())
	}
}

func TestProvider_Up_RejectsInvalidTargetPort(t *testing.T) {
	p := New()
	worktreeRoot := t.TempDir()
	repoDir := filepath.Join(worktreeRoot, "demo-api")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeRoot, "demo-api", "compose.yml"), []byte("services: {redis: {}}"), 0o644); err != nil {
		t.Fatalf("seed compose.yml: %v", err)
	}
	err := p.Up(context.Background(), &api.UpReq{
		WorktreeRoot: repoDir,
		Ports:        []api.PortSpec{{Role: "main", Port: 56123}},
		Extras: map[string]string{
			"compose.file":        "demo-api/compose.yml",
			"compose.service":     "redis",
			"compose.target_port": "not-a-number",
		},
	})
	if err == nil {
		t.Fatal("Up with a non-numeric compose.target_port = nil error, want an error")
	}
}

// TestProvider_Down_PassesGraceToComposeStop: a positive GracefulTimeoutSec
// becomes `docker compose stop -t N`, and the client deadline outlasts N so
// a stop that takes the full grace is not cut short. A fake docker on PATH
// records its arguments and sleeps 2 s on stop, past a 1 s grace.
func TestProvider_Down_PassesGraceToComposeStop(t *testing.T) {
	for _, tc := range []struct {
		name  string
		grace int
		want  string // substring of the recorded stop call
		avoid string
	}{
		{"positive grace", 1, "stop -t 1 redis", ""},
		{"zero keeps the compose default", 0, "stop redis", " -t "},
		// A service's stop_grace_period can exceed 10 s; zero must not cut
		// compose stop short of it.
		{"zero waits out a long compose grace", 0, "stop redis", " -t "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			record := filepath.Join(t.TempDir(), "calls.txt")
			t.Setenv("FAKE_DOCKER_RECORD", record)
			fake := "#!/bin/sh\necho \"$*\" >> \"$FAKE_DOCKER_RECORD\"\ncase \"$*\" in *\" stop \"*) sleep 2;; esac\n"
			switch {
			case strings.Contains(tc.name, "long"):
				if testing.Short() {
					t.Skip("sleeps 11 s")
				}
				fake = "#!/bin/sh\necho \"$*\" >> \"$FAKE_DOCKER_RECORD\"\ncase \"$*\" in *\" stop \"*) sleep 11;; esac\n"
			case tc.grace == 0:
				fake = "#!/bin/sh\necho \"$*\" >> \"$FAKE_DOCKER_RECORD\"\n"
			}
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

			worktreeRoot := t.TempDir()
			st := &upState{File: "compose.yml", Service: "redis", Project: "bough-t", TargetPort: 6379, HostPort: 56124}
			if err := writeSidecarState(worktreeRoot, st.HostPort, st); err != nil {
				t.Fatal(err)
			}
			err := New().Down(context.Background(), &api.DownReq{
				Ports: []int{st.HostPort}, WorktreeRoot: worktreeRoot, GracefulTimeoutSec: tc.grace,
			})
			if err != nil {
				t.Fatalf("Down: %v", err)
			}
			b, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			var stop string
			for _, line := range strings.Split(string(b), "\n") {
				if strings.Contains(line, " stop ") {
					stop = line
				}
			}
			if !strings.Contains(stop, tc.want) || (tc.avoid != "" && strings.Contains(stop, tc.avoid)) {
				t.Errorf("stop call = %q, want %q", stop, tc.want)
			}
		})
	}
}

// TestProvider_Down_BoundsHungCommands: a `compose stop` or `rm` that never
// returns (a pre_stop hook, a stuck daemon) gives up after cmdWait instead
// of holding the remove forever, even when a leftover child keeps the
// output pipe open.
func TestProvider_Down_BoundsHungCommands(t *testing.T) {
	for _, hang := range []string{"stop", "rm"} {
		t.Run(hang, func(t *testing.T) {
			bin := t.TempDir()
			record := filepath.Join(t.TempDir(), "calls.txt")
			t.Setenv("FAKE_DOCKER_RECORD", record)
			fake := "#!/bin/sh\necho \"$*\" >> \"$FAKE_DOCKER_RECORD\"\n" +
				"case \"$*\" in *\" " + hang + " \"*) sleep 60 & exec sleep 60;; esac\n"
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

			worktreeRoot := t.TempDir()
			st := &upState{File: "compose.yml", Service: "redis", Project: "bough-t", TargetPort: 6379, HostPort: 56125}
			if err := writeSidecarState(worktreeRoot, st.HostPort, st); err != nil {
				t.Fatal(err)
			}
			p := New()
			p.cmdWait = 5 * time.Second // spawning the fake docker can take seconds under load
			p.pipeWait = time.Second
			start := time.Now()
			err := p.Down(context.Background(), &api.DownReq{Ports: []int{st.HostPort}, WorktreeRoot: worktreeRoot})
			if d := time.Since(start); d > 30*time.Second {
				t.Errorf("Down took %v, want it bounded by cmdWait + pipeWait", d)
			}
			b, _ := os.ReadFile(record)
			if !strings.Contains(string(b), " "+hang+" ") {
				t.Fatalf("the fake never saw %s; calls:\n%s", hang, b)
			}
			if err == nil || !strings.Contains(err.Error(), "docker compose "+hang+" failed") {
				t.Fatalf("Down = %v, want the hung %s to be cut off", err, hang)
			}
		})
	}
}
