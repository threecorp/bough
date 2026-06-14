package pluginhost

import (
	"strings"
	"testing"
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
