package pluginhost

import (
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
)

func TestDiscover_pluginNotInPath(t *testing.T) {
	// A kind that maps to a nonexistent binary must surface a clear
	// "not found on PATH" error. The host CLI keys off this message
	// to nudge the user toward installing the right plugin.
	_, _, err := Discover("nonexistent-engine-99")
	if err == nil {
		t.Fatalf("expected error for absent plugin, got nil")
	}
	if !strings.Contains(err.Error(), "not found on PATH") {
		t.Errorf("error %q should explain the discovery failure", err.Error())
	}
	if !strings.Contains(err.Error(), "bough-plugin-nonexistent-engine-99") {
		t.Errorf("error %q should name the binary it expected", err.Error())
	}
}

// TestPluginLogLevel_defaultsToWarnAndHonorsEnv pins the quiet default:
// go-plugin's managed logger is Trace, which floods `bough create`
// stderr; bough forces Warn unless the operator opts into verbosity.
func TestPluginLogLevel_defaultsToWarnAndHonorsEnv(t *testing.T) {
	t.Setenv("BOUGH_PLUGIN_LOG", "")
	if got := pluginLogLevel(); got != hclog.Warn {
		t.Errorf("default level = %v, want Warn", got)
	}
	t.Setenv("BOUGH_PLUGIN_LOG", "debug")
	if got := pluginLogLevel(); got != hclog.Debug {
		t.Errorf("BOUGH_PLUGIN_LOG=debug → %v, want Debug", got)
	}
	t.Setenv("BOUGH_PLUGIN_LOG", "garbage")
	if got := pluginLogLevel(); got != hclog.Warn {
		t.Errorf("unrecognized BOUGH_PLUGIN_LOG → %v, want Warn fallback", got)
	}
}
