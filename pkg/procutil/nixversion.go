package procutil

import (
	"fmt"
	"strings"
)

// CheckNixVersion reports whether a requested engine version can run on
// the nix backend, whose package the bundled flake pins. It returns an
// actionable error when it cannot, and nil when it can.
//
// The flakes name a nixpkgs attribute (pkgs.mysql84, pkgs.postgresql_16),
// so the only version fact they carry is that attribute's line — the
// patch level comes from nix/flake.lock and moves on a lock update
// without a flake diff. Comparison is therefore by dotted component,
// over as many components as the shorter side has: "8", "8.4" and
// "8.4.5" all fall on a pinned "8.4", while "8.0" and "9" do not. An
// empty request passes, because the host only sends a version when the
// YAML declares one.
//
// Shared by the four engine plugins, whose nix Up paths ignored
// engines[].version outright before this existed.
func CheckNixVersion(engine, requested, pinned, attr string) error {
	if versionOnLine(requested, pinned) {
		return nil
	}
	return fmt.Errorf("%s: version %q cannot be honoured by the nix backend — the bundled flake pins %s %s (%s); set version: %q or backend: docker on this engine",
		engine, requested, engine, pinned, attr, pinned)
}

func versionOnLine(requested, pinned string) bool {
	if requested == "" {
		return true
	}
	req := strings.Split(requested, ".")
	pin := strings.Split(pinned, ".")
	for i := range min(len(req), len(pin)) {
		if req[i] != pin[i] {
			return false
		}
	}
	return true
}
