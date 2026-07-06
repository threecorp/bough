//go:build darwin || linux

package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	repoDir := filepath.Join(worktreeRoot, "auba-api")
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
	repoDir := filepath.Join(worktreeRoot, "auba-api")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	err := p.Up(context.Background(), &api.UpReq{
		WorktreeRoot: repoDir,
		Ports:        []api.PortSpec{{Role: "main", Port: 56123}},
		Extras: map[string]string{
			"compose.file":        "auba-api/does-not-exist.yml",
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

func TestProvider_Up_RejectsInvalidTargetPort(t *testing.T) {
	p := New()
	worktreeRoot := t.TempDir()
	repoDir := filepath.Join(worktreeRoot, "auba-api")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeRoot, "auba-api", "compose.yml"), []byte("services: {redis: {}}"), 0o644); err != nil {
		t.Fatalf("seed compose.yml: %v", err)
	}
	err := p.Up(context.Background(), &api.UpReq{
		WorktreeRoot: repoDir,
		Ports:        []api.PortSpec{{Role: "main", Port: 56123}},
		Extras: map[string]string{
			"compose.file":        "auba-api/compose.yml",
			"compose.service":     "redis",
			"compose.target_port": "not-a-number",
		},
	})
	if err == nil {
		t.Fatal("Up with a non-numeric compose.target_port = nil error, want an error")
	}
}
