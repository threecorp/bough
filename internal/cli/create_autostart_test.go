package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/config"
	engineapi "github.com/ikeikeikeike/bough/plugins/engine/api"
)

// countingProvider records which engine kinds reached Up.
type countingProvider struct {
	fakeEngineProvider
	kind string
	ups  *[]string
}

func (p *countingProvider) Up(context.Context, *engineapi.UpReq) error {
	*p.ups = append(*p.ups, p.kind)
	return nil
}

func countingDiscover(ups *[]string) func(string) (engineapi.EngineProvider, func(), error) {
	return func(kind string) (engineapi.EngineProvider, func(), error) {
		return &countingProvider{
			fakeEngineProvider: fakeEngineProvider{ready: true, envVars: map[string]string{"BOUGH_" + strings.ToUpper(kind) + "_HOST": "127.0.0.1"}},
			kind:               kind,
			ups:                ups,
		}, func() {}, nil
	}
}

func autostartEngine(kind string, low int, autostart *bool) config.Engine {
	return config.Engine{Kind: kind, PortRanges: map[string][2]int{"main": {low, low + 999}}, Autostart: autostart}
}

func gitRepoFixture(t *testing.T, root, name string) {
	t.Helper()
	repo := filepath.Join(root, name)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestRunCreate_Autostart(t *testing.T) {
	off, on := false, true
	tests := []struct {
		name          string
		mysql, redis  *bool
		startEngines  bool
		failingHook   bool
		wantUps       string
		wantHookRan   bool
		wantStrictErr bool
		wantLog       string
	}{
		{name: "default starts every engine", wantUps: "mysql,redis", wantHookRan: true},
		{name: "explicit true starts the engine", mysql: &on, redis: &on, wantUps: "mysql,redis", wantHookRan: true},
		{name: "a failing hook fails strict when every engine started", failingHook: true, wantUps: "mysql,redis", wantStrictErr: true},
		{name: "all off starts nothing and skips post_create", mysql: &off, redis: &off, wantLog: "post_create skipped"},
		{name: "one off starts the other and runs post_create", redis: &off, wantUps: "mysql", wantHookRan: true, wantLog: "redis: autostart is off"},
		{name: "a failing hook still fails strict while another engine is off", redis: &off, failingHook: true, wantUps: "mysql", wantStrictErr: true, wantLog: "--start-engines"},
		{name: "--start-engines overrides false", mysql: &off, redis: &off, startEngines: true, wantUps: "mysql,redis", wantHookRan: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			gitRepoFixture(t, root, "demo")
			hook := "touch hook-ran"
			if tt.failingHook {
				hook = "false"
			}
			cfg := &config.Config{
				Repositories: []config.Repository{{
					Name: "demo", BranchStrategy: "main", Role: "engine-provider",
					EnvLocal:   map[string]string{"DEMO_DB_PORT": "{{ .Mysql.Port }}"},
					PostCreate: []string{hook},
				}},
				Engines:  []config.Engine{autostartEngine("mysql", 42000, tt.mysql), autostartEngine("redis", 55000, tt.redis)},
				Registry: config.RegistryConfig{Path: ".bough/ports.json"},
			}
			var ups []string
			var stderr, stdout bytes.Buffer
			err := runCreate(context.Background(), &stderr, &stdout, cfg, root, "F-Auto", createOptions{
				noFetch: true, strict: true, startEngines: tt.startEngines, discover: countingDiscover(&ups),
			})

			if (err != nil) != tt.wantStrictErr {
				t.Fatalf("err = %v, wantStrictErr = %v\n%s", err, tt.wantStrictErr, stderr.String())
			}
			if got := strings.Join(ups, ","); got != tt.wantUps {
				t.Errorf("Up called for %q, want %q", got, tt.wantUps)
			}
			repo := filepath.Join(root, "worktrees/F-Auto/demo")
			if _, statErr := os.Stat(filepath.Join(repo, "hook-ran")); (statErr == nil) != tt.wantHookRan {
				t.Errorf("post_create ran = %v, want %v", statErr == nil, tt.wantHookRan)
			}
			if !strings.Contains(stderr.String(), tt.wantLog) {
				t.Errorf("stderr missing %q:\n%s", tt.wantLog, stderr.String())
			}
			// A stopped engine's port is still reserved and rendered, so
			// starting it later does not change .env.local's contract.
			env, readErr := os.ReadFile(filepath.Join(repo, ".env.local"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(env), "DEMO_DB_PORT=42") {
				t.Errorf(".env.local has no reserved mysql port:\n%s", env)
			}
			if want := filepath.Join(root, "worktrees", "F-Auto") + "\n"; stdout.String() != want {
				t.Errorf("stdout = %q, want exactly %q", stdout.String(), want)
			}
		})
	}
}

// Resuming a worktree re-runs create. post_create may have appended to
// .env.local, and it does not run when no engine starts, so a rewrite here
// would lose those lines.
func TestRunCreate_AllEnginesOffKeepsExistingEnvLocal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	gitRepoFixture(t, root, "demo")
	off := false
	cfg := &config.Config{
		Repositories: []config.Repository{{
			Name: "demo", BranchStrategy: "main", Role: "engine-provider",
			EnvLocal:   map[string]string{"DEMO_DB_PORT": "{{ .Mysql.Port }}"},
			PostCreate: []string{"echo APPENDED_BY_HOOK=1 >> .env.local"},
		}},
		Engines:  []config.Engine{autostartEngine("mysql", 42000, &off)},
		Registry: config.RegistryConfig{Path: ".bough/ports.json"},
	}
	var ups []string
	create := func(startEngines bool) {
		t.Helper()
		var stderr, stdout bytes.Buffer
		if err := runCreate(context.Background(), &stderr, &stdout, cfg, root, "F-Resume", createOptions{
			noFetch: true, strict: true, startEngines: startEngines, discover: countingDiscover(&ups),
		}); err != nil {
			t.Fatalf("create: %v\n%s", err, stderr.String())
		}
	}
	create(true)
	create(false)
	env, err := os.ReadFile(filepath.Join(root, "worktrees/F-Resume/demo/.env.local"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "APPENDED_BY_HOOK=1") {
		t.Errorf("resume with every engine off rewrote .env.local:\n%s", env)
	}
}
