package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMem0_Conformance builds the in-tree mock_mem0 plugin and
// runs the full MemoryBackend conformance suite against it. The
// mock_mem0 binary embeds an HTTP mem0 stub and serves the bough
// mem0 plugin over gRPC; see conformance/memory/mock_mem0/main.go
// for layout.
//
// This is the Ν-1.2 entry point promised by the v0.6 plan: the
// real mem0 cloud is never reached, the suite is hermetic, and
// the wire surface the bough plugin speaks is exactly the v1
// REST API the conformance suite cares about.
func TestMem0_Conformance(t *testing.T) {
	binPath := buildMockMem0(t)
	Run(t, Config{
		PluginBinary: binPath,
		Datadir:      t.TempDir(),
	})
}

// buildMockMem0 compiles mock_mem0/ into a one-off binary under
// t.TempDir() so the conformance suite spawns a fresh, isolated
// mock per run.
func buildMockMem0(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "mock-mem0-plugin")
	src := filepath.Join(testdataRoot(t), "mock_mem0")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build mock mem0 failed:\n%s\nerror: %v", out, err)
	}
	return bin
}
