package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestSelf builds the in-tree mock plugin and runs the full
// conformance suite against it. If the suite ever expands to
// require a new RPC behaviour, the mock either implements it or
// this self-test surfaces the gap before any real plugin author
// notices.
func TestSelf(t *testing.T) {
	mockPath := buildMockPlugin(t)
	Run(t, Config{
		PluginBinary: mockPath,
		Datadir:      t.TempDir(),
	})
}

func buildMockPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "mock-memory-plugin")
	// Resolve the mock_plugin/ source dir relative to this file.
	src := filepath.Join(testdataRoot(t), "mock_plugin")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build mock plugin failed:\n%s\nerror: %v", out, err)
	}
	return bin
}

func testdataRoot(t *testing.T) string {
	t.Helper()
	// Go's "go test" sets CWD to the package directory, so
	// mock_plugin/ is right next to us.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return wd
}
