package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/spf13/cobra"
)

// TestRetiredConfigKeys pins that the doctor finds every spelling of a
// retired top-level section the loader accepts, and ignores a nested key
// that merely shares a name.
func TestRetiredConfigKeys(t *testing.T) {
	cases := map[string]struct {
		yaml string
		want []string
	}{
		"block section":       {"instinct:\n  observer: {autostart: true}\n", []string{"instinct"}},
		"quoted key":          {"\"instinct\": {enabled: true}\n", []string{"instinct"}},
		"space before colon":  {"instinct : {}\nexport: {}\n", []string{"instinct", "export"}},
		"flow document":       {"{quality_gates: [], memory_backends: {}}\n", []string{"memory_backends", "quality_gates"}},
		"nested name not top": {"engines:\n  - kind: mysql\n    extras: {export: x}\n", nil},
		"nested block key":    {"registry:\n  export: x\n", nil},
		"none":                {"schema_version: 2\n", nil},
	}
	for name, tc := range cases {
		path := filepath.Join(t.TempDir(), ".bough.yaml")
		if err := os.WriteFile(path, []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := retiredConfigKeys(path)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: retiredConfigKeys = %v, want %v", name, got, tc.want)
		}
	}
}

// TestRetiredConfigKeys_UnparseableIsAnError keeps a broken file from
// reading as "no retired sections".
func TestRetiredConfigKeys_UnparseableIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".bough.yaml")
	if err := os.WriteFile(path, []byte("instinct: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := retiredConfigKeys(path); err == nil {
		t.Fatal("retiredConfigKeys on unparseable YAML: want an error, got nil")
	}
}

// TestLiveObserverPIDs reports a pid file whose process is alive and skips
// a stale or malformed one. The corpus path carries glob metacharacters,
// which must be read literally.
func TestLiveObserverPIDs(t *testing.T) {
	corpus := filepath.Join(t.TempDir(), "corpus[old]*")
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

// TestLiveObserverPIDs_OtherUsersProcessIsAlive: signal 0 to a process
// owned by another user fails with EPERM, which still means it exists.
func TestLiveObserverPIDs_OtherUsersProcessIsAlive(t *testing.T) {
	if err := syscall.Kill(1, 0); !errors.Is(err, syscall.EPERM) {
		t.Skipf("signal 0 to pid 1 returned %v here, not EPERM", err)
	}
	corpus := t.TempDir()
	d := filepath.Join(corpus, "projects", "p")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "observer.pid"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := liveObserverPIDs(corpus); len(got) != 1 || got[0] != 1 {
		t.Fatalf("liveObserverPIDs = %v, want [1]", got)
	}
}

// TestRenderRetiredConfig pins what the operator reads: no config file is
// not an error, a live PID is one to check rather than a confirmed daemon,
// and a broken .bough.yaml is reported instead of passing as clean.
func TestRenderRetiredConfig(t *testing.T) {
	corpus := t.TempDir()
	d := filepath.Join(corpus, "projects", "p")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "observer.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOUGH_HOMUNCULUS_DIR", corpus)
	dir := t.TempDir()
	t.Chdir(dir)

	var out bytes.Buffer
	renderRetiredConfig(&cobra.Command{}, &out)
	got := out.String()
	if strings.Contains(got, "could not check") {
		t.Errorf("a directory without .bough.yaml must not report an error:\n%s", got)
	}
	want := fmt.Sprintf("observer.pid names pid %d, which is still alive; if `pgrep -fl 'observer _run-daemon'` lists it", os.Getpid())
	if !strings.Contains(got, want) {
		t.Errorf("live PID must be reported as one to check:\n%s", got)
	}

	// A named config that is missing, and a default path that exists but
	// cannot be read, are both problems to report.
	named := &cobra.Command{}
	named.Flags().String("config", filepath.Join(dir, "missing.yaml"), "")
	out.Reset()
	renderRetiredConfig(named, &out)
	if !strings.Contains(out.String(), "missing.yaml: could not check for retired sections") {
		t.Errorf("a missing --config file must be reported:\n%s", out.String())
	}
	if err := os.Mkdir(filepath.Join(dir, ".bough.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	renderRetiredConfig(&cobra.Command{}, &out)
	if !strings.Contains(out.String(), "could not check for retired sections") {
		t.Errorf("an unreadable .bough.yaml must be reported:\n%s", out.String())
	}
	if err := os.Remove(filepath.Join(dir, ".bough.yaml")); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, ".bough.yaml"), []byte("instinct: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	renderRetiredConfig(&cobra.Command{}, &out)
	if !strings.Contains(out.String(), "could not check for retired sections") {
		t.Errorf("a broken .bough.yaml must be reported:\n%s", out.String())
	}
}

// TestRenderRetiredConfig_DanglingDefault: a .bough.yaml symlink whose target
// is gone is a broken config, not an absent one.
func TestRenderRetiredConfig_DanglingDefault(t *testing.T) {
	t.Setenv("BOUGH_HOMUNCULUS_DIR", filepath.Join(t.TempDir(), "absent"))
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Symlink(filepath.Join(dir, "gone.yaml"), filepath.Join(dir, ".bough.yaml")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	renderRetiredConfig(&cobra.Command{}, &out)
	if !strings.Contains(out.String(), "could not check for retired sections") || strings.Contains(out.String(), "none —") {
		t.Errorf("a dangling .bough.yaml symlink must be reported:\n%s", out.String())
	}
}

// TestRenderRetiredConfig_CleanState: no config, no corpus reads as none.
func TestRenderRetiredConfig_CleanState(t *testing.T) {
	t.Setenv("BOUGH_HOMUNCULUS_DIR", filepath.Join(t.TempDir(), "absent"))
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	renderRetiredConfig(&cobra.Command{}, &out)
	if !strings.Contains(out.String(), "none — no retired config sections, no leftover corpus") {
		t.Errorf("clean state must read as none:\n%s", out.String())
	}
}
