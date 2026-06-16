// Package backend implements host-side auto-detection of which DB
// plugin backend ("nix" vs "docker") should be used when the operator
// has not pinned `databases[].backend` explicitly in
// `.worktree-isolation.yaml`.
//
// The contract with the plugin layer is unchanged: bough still ships
// the chosen backend as `extras["backend"]` over the gRPC Up
// request, exactly as if the YAML had spelled it out. Detection only
// fills in the gap when the YAML leaves the field blank.
//
// Detection order (deliberately preferring nix to keep parity with the
// v0.1.x default for existing monorepos):
//
//  1. `nix` on PATH AND `nix-command` + `flakes` both enabled in the
//     user's nix config → "nix".
//  2. `docker info` exits 0 → "docker".
//  3. Neither reachable → a single, actionable error that names the
//     YAML knob the operator can use to bypass detection.
package backend

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// detectTimeout caps the time we spend probing each candidate backend.
// `docker info` against an unresponsive daemon can hang indefinitely;
// `nix config show` against a cold store is fast but the timeout keeps
// pathological cases (NFS-mounted nix store, broken sandbox) bounded.
const detectTimeout = 5 * time.Second

// ErrNoBackend is returned by Detect when neither nix-with-flakes nor a
// reachable docker daemon could be found. Wrapped errors carry the
// underlying exec failure for diagnostics; callers should prefer
// errors.Is(err, ErrNoBackend) over substring matching.
var ErrNoBackend = errors.New("backend: no usable backend found")

// Detect returns "nix" or "docker" based on host capability, or
// ErrNoBackend wrapped with the original probe failures. The result is
// stable across calls within a single process invocation (callers
// should cache it themselves — this package does not memoise so unit
// tests stay deterministic).
func Detect(ctx context.Context) (string, error) {
	if hasNixWithFlakes(ctx) {
		return "nix", nil
	}
	if dockerErr := hasDocker(ctx); dockerErr == nil {
		return "docker", nil
	} else {
		return "", fmt.Errorf("%w: neither nix (with flakes) nor docker daemon is reachable; install one or set databases[].backend explicitly in .worktree-isolation.yaml (docker probe: %v)", ErrNoBackend, dockerErr)
	}
}

// hasNixWithFlakes returns true iff the `nix` binary is on PATH AND
// both the `nix-command` and `flakes` experimental features are
// enabled in the active nix config.
//
// We use `exec.LookPath` rather than `nix --version` for the
// path-presence check because LookPath only stats the filesystem
// (~microseconds) vs. forking a subprocess (~5ms). Once nix is known to
// exist, `nix config show experimental-features` is the cheapest way
// to verify flakes are enabled — older nix without `config show`
// returns a non-zero exit, which is correctly classified as "flakes
// not usable".
func hasNixWithFlakes(ctx context.Context) bool {
	if _, err := exec.LookPath("nix"); err != nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, "nix", "config", "show", "experimental-features").Output()
	if err != nil {
		return false
	}
	// `nix config show experimental-features` prints the active feature
	// list on a single space-separated line (e.g. "fetch-tree flakes
	// nix-command"). We need BOTH nix-command AND flakes — either alone
	// is insufficient for bough's flake-driven Up paths.
	features := strings.Fields(strings.TrimSpace(string(out)))
	hasNixCommand := false
	hasFlakes := false
	for _, f := range features {
		switch f {
		case "nix-command":
			hasNixCommand = true
		case "flakes":
			hasFlakes = true
		}
	}
	return hasNixCommand && hasFlakes
}

// hasDocker returns nil when `docker info` exits 0 within
// detectTimeout, else the underlying exec error. We use `docker info`
// rather than `docker version` because version succeeds whenever the
// docker CLI is installed (even with no daemon), whereas info round-
// trips to the daemon and is the canonical "is docker actually
// usable" probe.
func hasDocker(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not on PATH: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "docker", "info")
	// Discard stdout/stderr; we only care about exit code. Leaving the
	// default pipes attached would buffer a few KB of docker info
	// output per probe needlessly.
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
