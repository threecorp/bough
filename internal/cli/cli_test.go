package cli

import (
	"bytes"
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
