package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// TestRetiredConfigKeys pins that the doctor finds every spelling of a
// retired top-level section the loader accepts, and ignores a nested key
// that merely shares a name.
func TestRetiredConfigKeys(t *testing.T) {
	cases := map[string]struct {
		yaml string
		want []string
	}{
		"block section":         {"instinct:\n  observer: {autostart: true}\n", []string{"instinct"}},
		"quoted key":            {"\"instinct\": {enabled: true}\n", []string{"instinct"}},
		"space before colon":    {"instinct : {}\nexport: {}\n", []string{"instinct", "export"}},
		"flow document":         {"{quality_gates: [], memory_backends: {}}\n", []string{"memory_backends", "quality_gates"}},
		"nested name not top":   {"engines:\n  - kind: mysql\n    extras: {export: x}\n", nil},
		"nested block key":      {"registry:\n  export: x\n", nil},
		"none":                  {"schema_version: 2\n", nil},
		"unparseable is silent": {"instinct: [\n", nil},
	}
	for name, tc := range cases {
		path := filepath.Join(t.TempDir(), ".bough.yaml")
		if err := os.WriteFile(path, []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := retiredConfigKeys(path); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: retiredConfigKeys = %v, want %v", name, got, tc.want)
		}
	}
}

// TestLiveObserverPIDs reports a pid file whose process is alive and skips
// a stale one, so doctor names only a daemon that is really still running.
func TestLiveObserverPIDs(t *testing.T) {
	corpus := t.TempDir()
	write := func(project, content string) {
		d := filepath.Join(corpus, "projects", project)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "observer.pid"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("alive", strconv.Itoa(os.Getpid()))
	write("stale", "999999")
	write("garbage", "not-a-pid")
	got := liveObserverPIDs(corpus)
	if len(got) != 1 || got[0] != os.Getpid() {
		t.Fatalf("liveObserverPIDs = %v, want [%d]", got, os.Getpid())
	}
}
