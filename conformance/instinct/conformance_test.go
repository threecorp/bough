package instinct

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSelf(t *testing.T) {
	mockPath := buildMockPlugin(t)
	Run(t, Config{PluginBinary: mockPath})
}

func buildMockPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "mock-instinct-plugin")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	src := filepath.Join(wd, "mock_plugin")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build mock plugin failed:\n%s\nerror: %v", out, err)
	}
	return bin
}
