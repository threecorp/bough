package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pluginBinDir writes fake plugins onto an otherwise empty PATH.
// Each entry maps a kind to the shell body of its binary.
func pluginBinDir(t *testing.T, bodies map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for kind, body := range bodies {
		path := filepath.Join(dir, "bough-plugin-"+kind)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

// A go-plugin binary launched without its handshake refuses and exits
// non-zero. That is a HEALTHY plugin — reporting it as broken would make
// the check cry wolf on every correct install.
func TestEnginePlugins_RefusingToRunWithoutAHandshakeIsHealthy(t *testing.T) {
	pluginBinDir(t, map[string]string{
		"mysql": `echo "This binary is a plugin. These are not meant to be executed directly." >&2; exit 1`,
		"redis": `echo "This binary is a plugin." >&2; exit 1`,
	})

	var buf bytes.Buffer
	renderEnginePlugins(context.Background(), &buf)

	out := buf.String()
	if !strings.Contains(out, "2 of 2 start") {
		t.Errorf("healthy plugins were not reported as starting:\n%s", out)
	}
	if strings.Contains(out, "cannot start") {
		t.Errorf("a plugin that refused the handshake was called broken:\n%s", out)
	}
}

// The incident this check exists for: the files are all present and
// executable, and the OS kills them on exec. Every other bough surface
// stays green through it, so doctor is the only place that can say so.
func TestEnginePlugins_ABinaryKilledOnExecIsReportedBroken(t *testing.T) {
	pluginBinDir(t, map[string]string{
		"mysql": `exit 1`,
		"redis": `kill -9 $$`,
	})

	var buf bytes.Buffer
	renderEnginePlugins(context.Background(), &buf)

	out := buf.String()
	if !strings.Contains(out, "1 of 2 start") {
		t.Errorf("the killed plugin was counted as starting:\n%s", out)
	}
	if !strings.Contains(out, "cannot start") || !strings.Contains(out, "redis") {
		t.Errorf("the killed plugin was not named:\n%s", out)
	}
	if strings.Contains(out, "mysql (") {
		t.Errorf("the healthy plugin was reported broken too:\n%s", out)
	}
}

// With nothing installed there is no subject to judge, and a section
// reading "0 of 0 start" is noise on every run of an engine-less setup.
func TestEnginePlugins_NoPluginsInstalledPrintsNothing(t *testing.T) {
	pluginBinDir(t, map[string]string{})

	var buf bytes.Buffer
	renderEnginePlugins(context.Background(), &buf)

	if buf.Len() != 0 {
		t.Errorf("expected silence with no plugins installed, got:\n%s", buf.String())
	}
}
