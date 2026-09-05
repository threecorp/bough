package procutil

import (
	"strings"
	"testing"
)

// TestCheckNixVersion pins the prefix rule the four engine plugins use
// to decide whether a declared version can run on the nix backend.
// Before it existed the nix paths ignored engines[].version outright, so
// `version: "17"` on postgres silently started the flake's pinned 16.
func TestCheckNixVersion(t *testing.T) {
	cases := []struct {
		requested string
		pinned    string
		wantOK    bool
	}{
		{"8.4", "8.4", true},
		{"8", "8.4", true},
		{"8.4.5", "8.4", true},
		{"8.0", "8.4", false},
		{"9", "8.4", false},
		{"16", "16", true},
		{"16.4", "16", true},
		{"17", "16", false},
		{"7.17.29", "7", true},
		{"9.4.1", "7", false},
		{"", "8", true},
		{"7-alpine", "8", false},
	}
	for _, c := range cases {
		err := CheckNixVersion("mysql", c.requested, c.pinned, "pkgs.mysql84")
		if gotOK := err == nil; gotOK != c.wantOK {
			t.Errorf("CheckNixVersion(%q, pinned %q) error = %v, want ok=%v", c.requested, c.pinned, err, c.wantOK)
		}
	}
}

// TestCheckNixVersionErrorIsActionable guards the wording: an operator
// reading it must learn what was rejected, what the flake actually
// runs, and the two ways out.
func TestCheckNixVersionErrorIsActionable(t *testing.T) {
	err := CheckNixVersion("elasticsearch", "9.4.1", "7", "pkgs.elasticsearch7")
	if err == nil {
		t.Fatal("CheckNixVersion accepted a version off the pinned line")
	}
	for _, want := range []string{"elasticsearch", `"9.4.1"`, "pkgs.elasticsearch7", "backend: docker"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
