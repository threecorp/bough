package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewRootCmd_smoke(t *testing.T) {
	// `bough --version` and `bough --help` must work without any YAML
	// being present — they are how new operators discover the binary
	// at all.
	cases := []string{"--version", "--help"}
	for _, arg := range cases {
		t.Run(arg, func(t *testing.T) {
			root := NewRootCmd("0.0.0-test")
			buf := &bytes.Buffer{}
			root.SetOut(buf)
			root.SetErr(buf)
			root.SetArgs([]string{arg})
			if err := root.Execute(); err != nil {
				t.Fatalf("%s: %v", arg, err)
			}
			if buf.Len() == 0 {
				t.Errorf("%s: no output", arg)
			}
		})
	}
}

func TestConfigValidate_acceptsDemoLikeFixture(t *testing.T) {
	// The example fixture lives next to the config package; just
	// point `bough config validate` at it.
	fix := filepath.Join("..", "config", "testdata", "example.yaml")
	if _, err := os.Stat(fix); err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	root := NewRootCmd("0.0.0-test")
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"config", "validate", fix})
	if err := root.Execute(); err != nil {
		t.Fatalf("validate: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "valid") {
		t.Errorf("expected 'valid' in output, got %q", buf.String())
	}
}

// TestConfigValidate_noArgsSurfacesRealLoadError is the regression
// guard for the wave-3 review finding (independently reported for
// merged PR #1 and again for PR #18): when no path argument is given
// and loadConfigAndRoot fails for a real reason (here, a malformed
// YAML in the default-discovery cwd), the command used to discard
// that error and always report the generic, actively-wrong "path
// argument missing" message instead — even though the path DID
// resolve; only loading it failed.
func TestConfigValidate_noArgsSurfacesRealLoadError(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".bough.yaml", []byte("schema_version: 1\n"), 0o644); err != nil {
		t.Fatalf("seed bad yaml: %v", err)
	}
	root := NewRootCmd("0.0.0-test")
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"config", "validate"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for malformed .bough.yaml, got nil")
	}
	if strings.Contains(err.Error(), "path argument missing") {
		t.Errorf("error misreported as \"path missing\" instead of the real load failure: %v", err)
	}
}

func TestConfigValidate_rejectsBadYAML(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("schema_version: 1\n"), 0o644); err != nil {
		t.Fatalf("seed bad yaml: %v", err)
	}
	root := NewRootCmd("0.0.0-test")
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"config", "validate", bad})
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected error on minimal YAML, got nil")
	}
}

func TestList_emptyRegistry(t *testing.T) {
	// Synthesise a minimal monorepo with a valid YAML + empty
	// registry; `bough list` should report the empty state rather than
	// blow up.
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".worktree-isolation.yaml"), []byte(`schema_version: 1
monorepo_root: "."
repositories:
  - {name: a, branch_strategy: develop}
registry: {path: .worktree-ports.json}
`), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	prev, _ := os.Getwd()
	defer func() { _ = os.Chdir(prev) }()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	root := NewRootCmd("0.0.0-test")
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"list"})
	if err := root.Execute(); err != nil {
		t.Fatalf("list: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "empty") {
		t.Errorf("expected 'empty' notice, got %q", buf.String())
	}
}

// captureStderr runs fn with os.Stderr redirected, since config.Load and
// resolveConfigPath write their warnings there, not to the command's writer.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	_ = r.Close()
	return string(out)
}

// TestConfigValidate_WarnsOnce: with no path argument, validate used to
// load the config twice and print every load warning twice.
func TestConfigValidate_WarnsOnce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".bough.yaml"), []byte(`schema_version: 2
monorepo_root: "."
repositories:
  - {name: a, branch_strategy: develop, role: engine-provider}
engines:
  - {kind: mysql, version: "8.4", socket_dir: /tmp, port_ranges: {main: [42000, 42999]}}
registry: {path: .bough-ports.json}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	var out bytes.Buffer
	stderr := captureStderr(t, func() {
		root := NewRootCmd("0.0.0-test")
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"config", "validate"})
		if err := root.Execute(); err != nil {
			t.Errorf("validate: %v\n%s", err, out.String())
		}
	})
	if n := strings.Count(stderr, "socket_dir has no effect"); n != 1 {
		t.Errorf("load warning printed %d times, want 1:\n%s", n, stderr)
	}
	if !strings.Contains(out.String(), ": valid") {
		t.Errorf("want a valid line, got %q", out.String())
	}
}

// TestConfigValidate_ReportsTheFileItRead: monorepo_root moves the root,
// not the file; validate must name the file it actually loaded.
func TestConfigValidate_ReportsTheFileItRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".bough.yaml"), []byte(`schema_version: 2
monorepo_root: "sub"
repositories:
  - {name: a, branch_strategy: develop}
registry: {path: .bough-ports.json}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	var out bytes.Buffer
	root := NewRootCmd("0.0.0-test")
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"config", "validate"})
	if err := root.Execute(); err != nil {
		t.Fatalf("validate: %v\n%s", err, out.String())
	}
	if want := filepath.Join(dir, ".bough.yaml") + ": valid"; !strings.Contains(out.String(), want) {
		t.Errorf("got %q, want it to name %s", out.String(), want)
	}
}

// TestCreateRemove_RejectPositionalArgs: `bough remove foo` used to ignore
// "foo" silently; both commands take the worktree only through flags.
func TestCreateRemove_RejectPositionalArgs(t *testing.T) {
	for _, sub := range []string{"create", "remove"} {
		root := NewRootCmd("0.0.0-test")
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{sub, "F-Feature"})
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("%s with a positional argument: want an unknown-command error, got %v\n%s", sub, err, out.String())
		}
	}
}

// TestLegacyConfigWarning_DoesNotClaimRemoval: .worktree-isolation.yaml is
// still read, so the warning must not say it was removed.
func TestLegacyConfigWarning_DoesNotClaimRemoval(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".worktree-isolation.yaml"), []byte("schema_version: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t, func() {
		if got := resolveConfigPath(NewRootCmd("0.0.0-test"), dir); filepath.Base(got) != ".worktree-isolation.yaml" {
			t.Errorf("resolveConfigPath = %s, want the legacy file", got)
		}
	})
	if strings.Contains(stderr, "removed in") || !strings.Contains(stderr, "rename to .bough.yaml") {
		t.Errorf("legacy warning = %q", stderr)
	}
}
