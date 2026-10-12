//go:build darwin || linux

package compose

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// fakeDockerPS puts a fake docker on PATH that records every call and
// answers `docker ps` with the output of psScript, a shell snippet that
// sees the ps arguments in "$*". It returns the call record's path.
func fakeDockerPS(t *testing.T, psScript string) string {
	t.Helper()
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "calls.txt")
	t.Setenv("FAKE_DOCKER_RECORD", record)
	fake := "#!/bin/sh\necho \"$*\" >> \"$FAKE_DOCKER_RECORD\"\ncase \"$1\" in ps) " + psScript + ";; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

// TestProvider_Down_LeavesAnotherWorktreesContainerAlone: container_name is
// bough-compose-<port> and the project name was the worktree name, so a
// second clone's same-named worktree reached the first clone's service;
// stopping it took that worktree's service down. A container made before
// the worktree label is recognized by the compose file compose recorded.
func TestProvider_Down_LeavesAnotherWorktreesContainerAlone(t *testing.T) {
	other := t.TempDir()
	otherFile := filepath.Join(other, "demo-api", "compose.yml")
	if err := os.MkdirAll(filepath.Dir(otherFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, row := range map[string]string{
		"labelled":                    "c1\\tbough-t\\t" + filepath.Join(other, "demo-api") + "\\t",
		"legacy":                      "c1\\tbough-t\\t\\t" + otherFile,
		"legacy whose files are gone": "c1\\tbough-t\\t\\t" + filepath.Join(other, "gone", "compose.yml"),
		"no labels at all":            "c1\\tbough-t\\t\\t",
	} {
		t.Run(name, func(t *testing.T) {
			record := fakeDockerPS(t, `printf '`+row+`\n'`)
			worktreeRoot := filepath.Join(t.TempDir(), "demo-api")
			st := &upState{File: "compose.yml", Service: "redis", Project: "bough-t", TargetPort: 6379, HostPort: 56127}
			if err := writeSidecarState(worktreeRoot, st.HostPort, st); err != nil {
				t.Fatal(err)
			}
			err := New().Down(context.Background(), &api.DownReq{Ports: []int{st.HostPort}, WorktreeRoot: worktreeRoot})
			if err == nil {
				t.Error("Down accepted a container not provably this worktree's")
			}
			if b, _ := os.ReadFile(record); strings.Contains(string(b), " stop ") || strings.Contains(string(b), " rm ") {
				t.Errorf("Down stopped another worktree's container; calls:\n%s", b)
			}
		})
	}
}

// TestProvider_Down_StopsItsOwnLegacyContainer: a container made before
// the worktree label is this worktree's when compose recorded this
// worktree's own override file on it (a shared compose file is no proof).
func TestProvider_Down_StopsItsOwnLegacyContainer(t *testing.T) {
	worktreeRoot := filepath.Join(t.TempDir(), "demo-api")
	// Down resolves the sidecar's compose.yml under the raw worktree root.
	ownFile := filepath.Join(filepath.Dir(worktreeRoot), "compose.yml")
	// An older bough started it with `-f compose.yml -f <override>`, so
	// compose recorded both; the override is the worktree's own.
	override := overrideFilePath(worktreeRoot, 56130)
	for _, f := range []string{ownFile, override} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	record := fakeDockerPS(t, `printf 'c1\tbough-t\t\t`+ownFile+`,`+override+`\n'`)
	st := &upState{File: "compose.yml", Service: "redis", Project: "bough-t", TargetPort: 6379, HostPort: 56130}
	if err := writeSidecarState(worktreeRoot, st.HostPort, st); err != nil {
		t.Fatal(err)
	}
	if err := New().Down(context.Background(), &api.DownReq{Ports: []int{st.HostPort}, WorktreeRoot: worktreeRoot}); err != nil {
		t.Fatalf("Down of this worktree's own legacy container: %v", err)
	}
	if b, _ := os.ReadFile(record); !strings.Contains(string(b), " stop ") {
		t.Errorf("Down never stopped its own container; calls:\n%s", b)
	}
}

// TestProvider_Up_RefusesAnotherWorktreesLegacyContainer: UpOrReuse reads
// only the bough label and datadir binds, so a legacy compose container of
// another clone under the same container name was reused, or removed when
// stopped. The compose owner check runs first.
func TestProvider_Up_RefusesAnotherWorktreesLegacyContainer(t *testing.T) {
	other := t.TempDir()
	otherFile := filepath.Join(other, "demo-api", "compose.yml")
	record := fakeDockerPS(t, `case "$*" in *name=*) printf 'c1\tbough-old\t\t`+otherFile+`\n';; esac`)
	worktreeRoot := filepath.Join(t.TempDir(), "demo-api")
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeRoot, "compose.yml"), []byte("services: {redis: {image: redis:7-alpine}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := New().Up(context.Background(), &api.UpReq{
		WorktreeRoot: worktreeRoot,
		Ports:        []api.PortSpec{{Role: "main", Port: 56131}},
		Extras:       map[string]string{"compose.file": "demo-api/compose.yml", "compose.service": "redis", "compose.target_port": "6379"},
	})
	if err == nil || !strings.Contains(err.Error(), "belongs to") {
		t.Errorf("Up = %v, want the owner refusal", err)
	}
	if b, _ := os.ReadFile(record); strings.Contains(string(b), "compose -f") {
		t.Errorf("Up ran docker compose after the refusal; calls:\n%s", b)
	}
}

// TestProvider_Up_KeepsTheRecordedProjectEvenWhenShared: switching a
// recorded project to a new one left the running container in the old
// project while the sidecar named the new one, so remove could not stop
// it. A project that holds another worktree's container is refused.
func TestProvider_Up_KeepsTheRecordedProjectEvenWhenShared(t *testing.T) {
	other := filepath.Join(t.TempDir(), "demo-api")
	const recorded = "bough-f-x-demo-api-compose-yml"
	record := fakeDockerPS(t, `case "$*" in *project=`+recorded+`*) printf 'c2\t`+recorded+`\t`+other+`\t\n';; esac`)
	worktreeRoot := filepath.Join(t.TempDir(), "demo-api")
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeRoot, "compose.yml"), []byte("services: {redis: {image: redis:7-alpine}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &upState{File: "demo-api/compose.yml", Service: "redis", Project: recorded, TargetPort: 6379, HostPort: 56132}
	if err := writeSidecarState(worktreeRoot, st.HostPort, st); err != nil {
		t.Fatal(err)
	}
	err := New().Up(context.Background(), &api.UpReq{
		WorktreeRoot: worktreeRoot,
		Ports:        []api.PortSpec{{Role: "main", Port: st.HostPort}},
		Extras:       map[string]string{"compose.file": st.File, "compose.service": st.Service, "compose.target_port": "6379"},
	})
	if err == nil || !strings.Contains(err.Error(), recorded) {
		t.Errorf("Up = %v, want a refusal naming the shared project %s", err, recorded)
	}
	if got, _ := readSidecarState(worktreeRoot, st.HostPort); got == nil || got.Project != recorded {
		t.Errorf("the sidecar's project changed to %+v", got)
	}
	if b, _ := os.ReadFile(record); strings.Contains(string(b), "compose -f") {
		t.Errorf("Up ran docker compose; calls:\n%s", b)
	}
}

// TestForeignOwner_RecordedComposeFiles: docker compose joins the config
// file paths with commas, which a path may itself contain, and records
// them as the caller spelled them. A recorded file is this worktree's only
// when it is the same file as this worktree's override file.
func TestForeignOwner_RecordedComposeFiles(t *testing.T) {
	base := t.TempDir()
	worktreeRoot := filepath.Join(base, "feat,one", "demo-api")
	composeFile := filepath.Join(worktreeRoot, "compose.yml")
	override := filepath.Join(worktreeRoot, ".local", "bough-compose-1-override.yml")
	for _, f := range []string{composeFile, override} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	own := []string{override}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(filepath.Join(base, "feat,one"), alias); err != nil {
		t.Fatal(err)
	}
	// A foreign file whose single path ends with this worktree's path.
	foreign := filepath.Join(base, "foreign,") + override
	if err := os.MkdirAll(filepath.Dir(foreign), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(override)
	_, upperErr := os.Stat(upper)
	cases := []struct {
		name, files string
		ours        bool
	}{
		{"own files under a name with a comma", composeFile + "," + override, true},
		{"own override through a symlinked alias", filepath.Join(alias, "demo-api", ".local", "bough-compose-1-override.yml"), true},
		{"own override in another letter case (same file only on a case-insensitive filesystem)", upper, upperErr == nil},
		{"only the shared compose file, with another worktree's override", composeFile + "," + filepath.Join(base, "feat", "x-override.yml"), false},
		{"a foreign single path that ends with this worktree's path", foreign, false},
		{"another worktree's files", filepath.Join(base, "feat", "demo-api", "compose.yml"), false},
		{"nothing recorded", "", false},
	}
	for _, tc := range cases {
		got := foreignOwner(composeContainer{ID: "c1", ConfigFiles: tc.files}, worktreeRoot, own)
		if (got == "") != tc.ours {
			t.Errorf("%s: foreignOwner = %q, want ours=%v", tc.name, got, tc.ours)
		}
	}
}

// TestProvider_Down_ChecksOnlyItsOwnService: Down stops one service, so a
// shared project holding another worktree's dependency must not block the
// remove that the refusal tells the user to run.
func TestProvider_Down_ChecksOnlyItsOwnService(t *testing.T) {
	other := filepath.Join(t.TempDir(), "demo-api")
	record := fakeDockerPS(t, `case "$*" in *com.docker.compose.service=redis*) ;; *project=*) printf 'c9\tbough-t\t`+other+`\t\n';; esac`)
	worktreeRoot := filepath.Join(t.TempDir(), "demo-api")
	st := &upState{File: "compose.yml", Service: "redis", Project: "bough-t", TargetPort: 6379, HostPort: 56133}
	if err := writeSidecarState(worktreeRoot, st.HostPort, st); err != nil {
		t.Fatal(err)
	}
	if err := New().Down(context.Background(), &api.DownReq{Ports: []int{st.HostPort}, WorktreeRoot: worktreeRoot}); err != nil {
		t.Fatalf("Down refused over another worktree's dependency: %v", err)
	}
	if b, _ := os.ReadFile(record); !strings.Contains(string(b), " stop ") {
		t.Errorf("Down never stopped its own service; calls:\n%s", b)
	}
}

// TestProvider_Up_RefusesAChangedExplicitProject: switching compose.project
// while this worktree's container runs in the old one left the container
// there and the sidecar naming the new one.
func TestProvider_Up_RefusesAChangedExplicitProject(t *testing.T) {
	worktreeRoot := filepath.Join(t.TempDir(), "demo-api")
	record := fakeDockerPS(t, `case "$*" in *name=*) printf 'c1\told\t`+worktreeRoot+`\t\trunning\n';; esac`)
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeRoot, "compose.yml"), []byte("services: {redis: {image: redis:7-alpine}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := New().Up(context.Background(), &api.UpReq{
		WorktreeRoot: worktreeRoot,
		Ports:        []api.PortSpec{{Role: "main", Port: 56134}},
		Extras:       map[string]string{"compose.file": "demo-api/compose.yml", "compose.service": "redis", "compose.target_port": "6379", "compose.project": "new"},
	})
	if err == nil || !strings.Contains(err.Error(), "old") {
		t.Errorf("Up = %v, want a refusal naming the running project old", err)
	}
	if b, _ := os.ReadFile(record); strings.Contains(string(b), "compose -f") {
		t.Errorf("Up ran docker compose; calls:\n%s", b)
	}
}

// TestResolveProject_ChangedProjectIsRefusedInEveryState: a restarting or
// paused container counts as running for reuse, and recreating a stopped
// one could fail with the old sidecar left naming the old project, so any
// container of this worktree pins the project until `bough remove`.
func TestResolveProject_ChangedProjectIsRefusedInEveryState(t *testing.T) {
	worktreeRoot := filepath.Join(t.TempDir(), "demo-api")
	for _, state := range []string{"running", "restarting", "paused", "exited", "created"} {
		fakeDockerPS(t, `case "$*" in *name=*) printf 'c1\told\t`+worktreeRoot+`\t\t`+state+`\n';; esac`)
		_, err := New().resolveProject(context.Background(), worktreeRoot, 56135, "demo-api/compose.yml", "redis", "new", nil)
		if err == nil || !strings.Contains(err.Error(), "bough remove") {
			t.Errorf("%s: resolveProject = %v, want a refusal that points at bough remove", state, err)
		}
	}
}

// TestCheckOwner_RefusalNeverAdvisesDeletingTheOtherContainer: the refused
// container may be another worktree's; the message must not tell the user
// to delete it.
func TestCheckOwner_RefusalNeverAdvisesDeletingTheOtherContainer(t *testing.T) {
	other := filepath.Join(t.TempDir(), "demo-api")
	fakeDockerPS(t, `case "$*" in *name=*) printf 'c1\tp\t`+other+`\t\n';; esac`)
	err := New().checkOwner(context.Background(), 56136, filepath.Join(t.TempDir(), "demo-api"), "p", "", nil)
	if err == nil {
		t.Fatal("checkOwner accepted another worktree's container")
	}
	if strings.Contains(err.Error(), "docker rm") {
		t.Errorf("refusal advises deleting a container: %v", err)
	}
}

// TestProvider_Down_RemovesItsOwnContainerWithoutASidecar: an Up that
// failed after starting the container but before writing the sidecar left
// Down with nothing to stop, so the published port blocked remove. The
// container is removed by the ID that was checked, never by the name a
// newer container may carry; one already gone is not an error, and any
// other daemon error is.
func TestProvider_Down_RemovesItsOwnContainerWithoutASidecar(t *testing.T) {
	worktreeRoot := filepath.Join(t.TempDir(), "demo-api")
	row := `printf 'c1id\tp\t` + worktreeRoot + `\t\trunning\n'`
	for _, tc := range []struct {
		name, rm string
		wantErr  bool
	}{
		{"removed", "true", false},
		{"already gone", "echo 'Error response from daemon: No such container: c1id' >&2; exit 1", false},
		{"daemon error", "echo 'Error response from daemon: boom' >&2; exit 1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			record := filepath.Join(t.TempDir(), "calls.txt")
			t.Setenv("FAKE_DOCKER_RECORD", record)
			fake := "#!/bin/sh\necho \"$*\" >> \"$FAKE_DOCKER_RECORD\"\ncase \"$1\" in ps) case \"$*\" in *name=*) " + row + ";; esac;; rm) " + tc.rm + ";; esac\n"
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			err := New().Down(context.Background(), &api.DownReq{Ports: []int{56137}, WorktreeRoot: worktreeRoot})
			if (err != nil) != tc.wantErr {
				t.Errorf("Down = %v, want error %v", err, tc.wantErr)
			}
			b, _ := os.ReadFile(record)
			if !strings.Contains(string(b), "rm -f c1id") {
				t.Errorf("Down did not remove its own container by ID; calls:\n%s", b)
			}
			if strings.Contains(string(b), "rm -f bough-compose-56137") {
				t.Errorf("Down removed by name, which another container may carry; calls:\n%s", b)
			}
			if !tc.wantErr && !strings.Contains(string(b), "network ls --no-trunc --format {{.ID}}\t{{.Name}} --filter label=com.docker.compose.project=p") {
				t.Errorf("Down never cleaned the removed container's project network; calls:\n%s", b)
			}
		})
	}
}

// TestProvider_Up_SharedComposeFileIsNoProofOfOwnership: two worktrees may
// share one compose file by absolute path; a dependency container that
// recorded it alongside another worktree's override is that worktree's.
func TestProvider_Up_SharedComposeFileIsNoProofOfOwnership(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared-compose.yml")
	if err := os.WriteFile(shared, []byte("services: {redis: {image: redis:7-alpine}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherOverride := filepath.Join(t.TempDir(), "demo-api", ".local", "bough-compose-1-override.yml")
	if err := os.MkdirAll(filepath.Dir(otherOverride), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherOverride, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	record := fakeDockerPS(t, `case "$*" in *project=shared*) printf 'dep1\tshared\t\t`+shared+`,`+otherOverride+`\n';; esac`)
	worktreeRoot := filepath.Join(t.TempDir(), "demo-api")
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	err := New().Up(context.Background(), &api.UpReq{
		WorktreeRoot: worktreeRoot,
		Ports:        []api.PortSpec{{Role: "main", Port: 56138}},
		Extras:       map[string]string{"compose.file": shared, "compose.service": "redis", "compose.target_port": "6379", "compose.project": "shared"},
	})
	if err == nil || !strings.Contains(err.Error(), "belongs to") {
		t.Errorf("Up = %v, want the owner refusal for the other worktree's dependency", err)
	}
	if b, _ := os.ReadFile(record); strings.Contains(string(b), "compose -f") {
		t.Errorf("Up ran docker compose; calls:\n%s", b)
	}
}

// TestProvider_Down_RemovesTheEmptyProjectNetwork: Down never runs
// compose down, so the project's network outlived its last container and
// every removed worktree left one behind until Docker ran out of address
// pools. Once the project holds no container, its own network goes; one
// another container still names (a stopped one of a project that joined
// it as external) stays, and one gone meanwhile is not an error.
func TestProvider_Down_RemovesTheEmptyProjectNetwork(t *testing.T) {
	const projectFilter = "label=com.docker.compose.project=bough-t"
	for _, tc := range []struct {
		name, ps, rm, inspect string
		removed, wantErr      bool
	}{
		{name: "project now empty", ps: "true", rm: "true", inspect: "true", removed: true},
		{name: "project still holds a sibling service", ps: `case "$*" in *service=*) ;; *project=bough-t*) printf 's1\tbough-t\t\t\tExited\n';; esac`, rm: "true", inspect: "true"},
		{name: "another project's stopped container names the network", ps: `case "$*" in *-a*network=net1*) printf 'x1\tother\t\t\texited\n';; esac`, rm: "true", inspect: "true"},
		{name: "a never-started container names it by name only", ps: `case "$*" in *-a*network=bough-t_default*) printf 'x2\tother\t\t\tcreated\n';; esac`, rm: "true", inspect: "true"},
		{name: "network gone meanwhile", ps: "true", rm: "echo 'Error response from daemon: unknown network net1' >&2; exit 1", inspect: "echo 'Error: No such network: net1' >&2; exit 1", removed: true},
		{name: "joined by another container meanwhile", ps: "true", rm: "echo 'Error response from daemon: error while removing network: network bough-t_default has active endpoints' >&2; exit 1", inspect: "true", removed: true},
		{name: "removal itself reports it gone, re-check unreachable", ps: "true", rm: "echo 'Error response from daemon: network net1 not found' >&2; exit 1", inspect: "echo 'Cannot connect to the Docker daemon' >&2; exit 1", removed: true},
		{name: "daemon error", ps: "true", rm: "echo 'Error response from daemon: boom' >&2; exit 1", inspect: "true", removed: true, wantErr: true},
		{name: "daemon unreachable for the re-check", ps: "true", rm: "echo 'Error response from daemon: boom' >&2; exit 1", inspect: "echo 'Cannot connect to the Docker daemon' >&2; exit 1", removed: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			record := filepath.Join(t.TempDir(), "calls.txt")
			t.Setenv("FAKE_DOCKER_RECORD", record)
			fake := "#!/bin/sh\necho \"$*\" >> \"$FAKE_DOCKER_RECORD\"\n" +
				"case \"$1 $2\" in\n" +
				"\"network ls\") case \"$*\" in *--no-trunc*" + projectFilter + "*) printf 'net1\\tbough-t_default\\n';; esac;;\n" +
				"\"network rm\") " + tc.rm + ";;\n" +
				"\"network inspect\") " + tc.inspect + ";;\n" +
				"ps*) " + tc.ps + ";;\n" +
				"esac\n"
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			worktreeRoot := filepath.Join(t.TempDir(), "demo-api")
			st := &upState{File: "compose.yml", Service: "redis", Project: "bough-t", TargetPort: 6379, HostPort: 56139}
			if err := writeSidecarState(worktreeRoot, st.HostPort, st); err != nil {
				t.Fatal(err)
			}
			err := New().Down(context.Background(), &api.DownReq{Ports: []int{st.HostPort}, WorktreeRoot: worktreeRoot})
			if (err != nil) != tc.wantErr {
				t.Errorf("Down = %v, want error %v", err, tc.wantErr)
			}
			b, _ := os.ReadFile(record)
			if got := strings.Contains(string(b), "network rm net1"); got != tc.removed {
				t.Errorf("network rm called = %v, want %v; calls:\n%s", got, tc.removed, b)
			}
		})
	}
}

// TestProvider_Down_PsFailureStopsNothing: an owner check that cannot list
// the project's containers must not let compose stop act on them.
func TestProvider_Down_PsFailureStopsNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ps    string
		stops bool
	}{
		{"daemon error", "echo 'Error response from daemon: boom' >&2; exit 1", false},
		{"nothing there", "true", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := fakeDockerPS(t, tc.ps)
			worktreeRoot := filepath.Join(t.TempDir(), "demo-api")
			st := &upState{File: "compose.yml", Service: "redis", Project: "bough-t", TargetPort: 6379, HostPort: 56128}
			if err := writeSidecarState(worktreeRoot, st.HostPort, st); err != nil {
				t.Fatal(err)
			}
			err := New().Down(context.Background(), &api.DownReq{Ports: []int{st.HostPort}, WorktreeRoot: worktreeRoot})
			b, _ := os.ReadFile(record)
			if stopped := strings.Contains(string(b), " stop "); stopped != tc.stops {
				t.Errorf("stop called = %v, want %v (Down err %v); calls:\n%s", stopped, tc.stops, err, b)
			}
			if !tc.stops && err == nil {
				t.Error("Down succeeded although docker ps failed")
			}
		})
	}
}

// TestResolveProject: an existing service keeps the project it was
// started under (from the sidecar, or from its container when the sidecar
// was never written), unless that project holds another worktree's
// container for the service, which two clones shared before the hash.
func TestResolveProject(t *testing.T) {
	worktreeRoot := filepath.Join(t.TempDir(), "demo-api")
	hashed := composeProjectName(filepath.Base(filepath.Dir(worktreeRoot)), "demo-api/compose.yml", filepath.Dir(worktreeRoot))
	const recorded = "bough-f-x-demo-api-compose-yml"
	cases := []struct {
		name    string
		sidecar bool
		ps      string
		want    string
	}{
		{"sidecar project with no foreign container", true, "true", recorded},

		{"no sidecar, own container records the project", false,
			`case "$*" in *name=*) printf 'c1\t` + recorded + `\t` + worktreeRoot + `\t\n';; esac`, recorded},
		{"no sidecar, no container", false, "true", hashed},
		{"docker ps fails", false, "echo boom >&2; exit 1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeDockerPS(t, tc.ps)
			root := worktreeRoot
			if tc.sidecar {
				st := &upState{File: "demo-api/compose.yml", Service: "redis", Project: recorded, TargetPort: 6379, HostPort: 56129}
				if err := writeSidecarState(root, st.HostPort, st); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(root, ".local")) })
			}
			got, err := New().resolveProject(context.Background(), root, 56129, "demo-api/compose.yml", "redis", "", nil)
			if tc.want == "" {
				if err == nil {
					t.Errorf("resolveProject = %q although docker ps failed, want an error", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("resolveProject = (%q, %v), want %q", got, err, tc.want)
			}
		})
	}
	if got, _ := New().resolveProject(context.Background(), worktreeRoot, 56129, "demo-api/compose.yml", "redis", "mine", nil); got != "mine" {
		t.Errorf("an explicit compose.project must win; got %q", got)
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
			child := filepath.Join(t.TempDir(), "child.pid")
			t.Setenv("FAKE_DOCKER_CHILD", child)
			t.Cleanup(func() { killPIDFile(child) })
			fake := "#!/bin/sh\necho \"$*\" >> \"$FAKE_DOCKER_RECORD\"\n" +
				"case \"$*\" in *\" " + hang + " \"*) sleep 60 & echo $! > \"$FAKE_DOCKER_CHILD\"; exec sleep 60;; esac\n"
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

// TestProvider_Down_ExitsZeroWithChildHoldingPipe: a `compose stop` or `rm`
// that exits 0 while a child still holds its output counts as success
// instead of failing on ErrWaitDelay.
func TestProvider_Down_ExitsZeroWithChildHoldingPipe(t *testing.T) {
	for _, cmd := range []string{"stop", "rm"} {
		t.Run(cmd, func(t *testing.T) {
			bin := t.TempDir()
			record := filepath.Join(t.TempDir(), "calls.txt")
			child := filepath.Join(t.TempDir(), "child.pid")
			t.Setenv("FAKE_DOCKER_RECORD", record)
			t.Setenv("FAKE_DOCKER_CHILD", child)
			t.Cleanup(func() { killPIDFile(child) })
			fake := "#!/bin/sh\necho \"$*\" >> \"$FAKE_DOCKER_RECORD\"\n" +
				"case \"$*\" in *\" " + cmd + " \"*) sleep 30 & echo $! > \"$FAKE_DOCKER_CHILD\";; esac\nexit 0\n"
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

			worktreeRoot := t.TempDir()
			st := &upState{File: "compose.yml", Service: "redis", Project: "bough-t", TargetPort: 6379, HostPort: 56126}
			if err := writeSidecarState(worktreeRoot, st.HostPort, st); err != nil {
				t.Fatal(err)
			}
			p := New()
			p.pipeWait = time.Second
			if err := p.Down(context.Background(), &api.DownReq{Ports: []int{st.HostPort}, WorktreeRoot: worktreeRoot}); err != nil {
				t.Fatalf("Down: %v", err)
			}
			if b, _ := os.ReadFile(record); !strings.Contains(string(b), " rm ") {
				t.Fatalf("rm never ran; calls:\n%s", b)
			}
		})
	}
}

// killPIDFile stops the background child a fake docker recorded.
func killPIDFile(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
