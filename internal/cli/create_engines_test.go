package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/config"
	engineapi "github.com/ikeikeikeike/bough/plugins/engine/api"
)

// fakeEngineProvider implements engineapi.EngineProvider with scripted
// per-phase results so startEngines' error wrapping is testable
// without spawning plugin subprocesses (#68).
type fakeEngineProvider struct {
	upErr    error
	ready    bool
	readyErr error
	envVars  map[string]string
	envErr   error
}

func (f *fakeEngineProvider) Up(context.Context, *engineapi.UpReq) error     { return f.upErr }
func (f *fakeEngineProvider) Down(context.Context, *engineapi.DownReq) error { return nil }
func (f *fakeEngineProvider) ReadyCheck(context.Context, []int, int) (bool, error) {
	return f.ready, f.readyErr
}
func (f *fakeEngineProvider) Cleanup(context.Context, string, []int) error { return nil }
func (f *fakeEngineProvider) PortRangeDefault(context.Context) (map[string]engineapi.PortRange, error) {
	return nil, nil
}

func (f *fakeEngineProvider) EnvVars(context.Context, *engineapi.EnvVarsReq) (map[string]string, error) {
	return f.envVars, f.envErr
}

// TestToPluginSpecs confirms config.EnginePlugin (the YAML-facing,
// validated type) converts field-for-field into engineapi.PluginSpec
// (the wire-facing type Up sends to the plugin), including the empty
// and nil-slice cases so an engine with no `plugins:` declared gets an
// empty (not nil-panicking) slice through toPluginSpecs → UpReq.Plugins
// → pluginSpecsToProto's own len()-based make().
func TestToPluginSpecs(t *testing.T) {
	cases := []struct {
		name string
		in   []config.EnginePlugin
		want []engineapi.PluginSpec
	}{
		{"nil in -> empty out", nil, []engineapi.PluginSpec{}},
		{"empty in -> empty out", []config.EnginePlugin{}, []engineapi.PluginSpec{}},
		{
			"official plugin (no location)",
			[]config.EnginePlugin{{ID: "analysis-icu"}},
			[]engineapi.PluginSpec{{ID: "analysis-icu"}},
		},
		{
			"unofficial plugin with location, multiple entries",
			[]config.EnginePlugin{
				{ID: "analysis-icu"},
				{ID: "analysis-sudachi", Location: "https://example.com/sudachi.zip"},
			},
			[]engineapi.PluginSpec{
				{ID: "analysis-icu"},
				{ID: "analysis-sudachi", Location: "https://example.com/sudachi.zip"},
			},
		},
	}
	for _, c := range cases {
		got := toPluginSpecs(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("%s: toPluginSpecs(%v) = %v, want %v", c.name, c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: toPluginSpecs(%v)[%d] = %+v, want %+v", c.name, c.in, i, got[i], c.want[i])
			}
		}
	}
}

// TestBuildEngineExtras_FlattensCompose is the regression guard for
// the kind: compose wiring: Engine.Compose is the only conduit for
// compose.file/service/target_port/project/env_prefix to reach
// UpReq.Extras, since UpReq.Extras (a map[string]string) is the sole
// channel from config to plugin — the typed ComposeSpec field exists
// purely for config-time validation and must still be flattened here.
func TestBuildEngineExtras_FlattensCompose(t *testing.T) {
	eng := config.Engine{
		Kind:    "compose",
		Version: "7-alpine",
		Compose: &config.ComposeSpec{
			File:       "auba-api/compose.yml",
			Service:    "redis",
			TargetPort: 6379,
		},
	}
	extras := buildEngineExtras(eng, "")
	want := map[string]string{
		"compose.file":        "auba-api/compose.yml",
		"compose.service":     "redis",
		"compose.target_port": "6379",
		"version":             "7-alpine",
	}
	for k, v := range want {
		if got := extras[k]; got != v {
			t.Errorf("extras[%q] = %q, want %q", k, got, v)
		}
	}
	if _, ok := extras["compose.project"]; ok {
		t.Errorf("compose.project should be absent when ComposeSpec.Project is empty, got %q", extras["compose.project"])
	}
	if _, ok := extras["compose.env_prefix"]; ok {
		t.Errorf("compose.env_prefix should be absent when ComposeSpec.EnvPrefix is empty, got %q", extras["compose.env_prefix"])
	}
}

// TestBuildEngineExtras_ComposeOptionalFields confirms Project/
// EnvPrefix pass through when the operator does set them.
func TestBuildEngineExtras_ComposeOptionalFields(t *testing.T) {
	eng := config.Engine{
		Kind: "compose",
		Compose: &config.ComposeSpec{
			File: "a/compose.yml", Service: "redis", TargetPort: 6379,
			Project: "my-project", EnvPrefix: "CACHE",
		},
	}
	extras := buildEngineExtras(eng, "")
	if got := extras["compose.project"]; got != "my-project" {
		t.Errorf("compose.project = %q, want %q", got, "my-project")
	}
	if got := extras["compose.env_prefix"]; got != "CACHE" {
		t.Errorf("compose.env_prefix = %q, want %q", got, "CACHE")
	}
}

// TestBuildEngineExtras_NonComposeEngineUnaffected guards against a
// future edit accidentally emitting compose.* keys for the other four
// engine kinds, which never set Engine.Compose.
func TestBuildEngineExtras_NonComposeEngineUnaffected(t *testing.T) {
	eng := config.Engine{Kind: "mysql", Backend: "docker"}
	extras := buildEngineExtras(eng, "")
	for k := range extras {
		if strings.HasPrefix(k, "compose.") {
			t.Errorf("non-compose engine got a compose.* extras key: %q", k)
		}
	}
}

// engineTestConfig declares one mysql engine with an explicit backend
// (so detectBackendIfNeeded never probes the real host) and a fixed
// ready timeout the timeout-message assertion can pin.
func engineTestConfig() *config.Config {
	return &config.Config{
		Engines: []config.Engine{{
			Kind:            "mysql",
			Backend:         "docker",
			ReadyTimeoutSec: 42,
		}},
	}
}

// TestStartEngines_WrapsPhaseErrors pins the `%s <phase>: %w` wrapping
// for every RPC phase. The EnvVars wrap was silently lost once in a
// refactor (24a4fbc, restored in bc514cf); errors.Is through the
// returned chain is the regression tripwire — a dropped %w keeps the
// message readable but breaks the unwrap.
func TestStartEngines_WrapsPhaseErrors(t *testing.T) {
	sentinel := errors.New("sentinel failure")
	tests := []struct {
		name        string
		discoverErr error
		prov        *fakeEngineProvider
		wantSubstr  string
		wantWrapped bool // sentinel must survive errors.Is through the chain
	}{
		{
			name:        "discover error",
			discoverErr: sentinel,
			wantSubstr:  "discover mysql plugin:",
			wantWrapped: true,
		},
		{
			name:        "Up error",
			prov:        &fakeEngineProvider{upErr: sentinel},
			wantSubstr:  "mysql Up:",
			wantWrapped: true,
		},
		{
			name:        "ReadyCheck error",
			prov:        &fakeEngineProvider{readyErr: sentinel},
			wantSubstr:  "mysql ReadyCheck:",
			wantWrapped: true,
		},
		{
			// (ready=false, err=nil) is a timeout: there is no underlying
			// error to wrap, so the formatted message itself is the
			// contract — and it must not leak a `%!w(<nil>)` verb.
			name:       "ReadyCheck timeout",
			prov:       &fakeEngineProvider{ready: false},
			wantSubstr: "mysql ReadyCheck: not ready within 42s",
		},
		{
			name:        "EnvVars error",
			prov:        &fakeEngineProvider{ready: true, envErr: sentinel},
			wantSubstr:  "mysql EnvVars:",
			wantWrapped: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			discover := func(kind string) (engineapi.EngineProvider, func(), error) {
				if kind != "mysql" {
					t.Fatalf("discover called with kind %q, want mysql", kind)
				}
				if tt.discoverErr != nil {
					return nil, nil, tt.discoverErr
				}
				return tt.prov, func() {}, nil
			}
			var buf bytes.Buffer
			engines, err := startEngines(
				context.Background(), &buf, engineTestConfig(),
				t.TempDir(), map[string]int{"mysql": 42001}, discover,
			)
			if err == nil {
				t.Fatal("startEngines returned nil error")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error %q missing %q", err, tt.wantSubstr)
			}
			if strings.Contains(err.Error(), "%!w") {
				t.Errorf("error %q leaked a bad format verb (nil %%w)", err)
			}
			if tt.wantWrapped && !errors.Is(err, sentinel) {
				t.Errorf("error chain lost the underlying error (dropped %%w?): %v", err)
			}
			// Contract: startEngines returns whatever it managed to bring
			// up — even on error — so the caller's defer can kill the
			// started subprocesses.
			if tt.discoverErr == nil {
				if len(engines) != 1 || engines[0].kill == nil {
					t.Errorf("engines = %d entries, want the discovered instance with its kill func", len(engines))
				}
			} else if len(engines) != 0 {
				t.Errorf("engines = %d entries on discover failure, want 0", len(engines))
			}
		})
	}
}

// TestStartEngines_HappyPathCollectsEnvVars: the success path stashes
// the provider's EnvVars on the instance (for the env-render pass) and
// logs readiness on the allocated port.
func TestStartEngines_HappyPathCollectsEnvVars(t *testing.T) {
	discover := func(string) (engineapi.EngineProvider, func(), error) {
		return &fakeEngineProvider{
			ready:   true,
			envVars: map[string]string{"BOUGH_MYSQL_PORT": "42001"},
		}, func() {}, nil
	}
	var buf bytes.Buffer
	engines, err := startEngines(
		context.Background(), &buf, engineTestConfig(),
		t.TempDir(), map[string]int{"mysql": 42001}, discover,
	)
	if err != nil {
		t.Fatalf("startEngines: %v", err)
	}
	if len(engines) != 1 {
		t.Fatalf("engines = %d, want 1", len(engines))
	}
	if got := engines[0].envVars["BOUGH_MYSQL_PORT"]; got != "42001" {
		t.Errorf("envVars not stashed on the instance: %v", engines[0].envVars)
	}
	if !strings.Contains(buf.String(), "mysql: ready on port 42001") {
		t.Errorf("missing ready log line: %q", buf.String())
	}
}
