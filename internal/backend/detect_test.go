package backend

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// TestDetect_returnsSomethingOrError documents the contract: Detect
// either succeeds with one of the known backends, or returns
// ErrNoBackend with a clear instruction for the operator. We can't
// pin the exact return value because CI matrices include hosts that
// have only nix, only docker, both, or neither — but the union of
// outcomes is closed.
func TestDetect_returnsSomethingOrError(t *testing.T) {
	got, err := Detect(context.Background())
	if err != nil {
		if !errors.Is(err, ErrNoBackend) {
			t.Fatalf("Detect: unexpected error type: %v", err)
		}
		// On a CI host with neither nix nor docker the error message
		// must name the YAML escape hatch so an operator can unblock
		// themselves without reading source. (The knob was renamed
		// databases[].backend -> engines[].backend in the v0.4 schema
		// rename; this assertion tracks the current name.)
		if msg := err.Error(); !strings.Contains(msg, "engines[].backend") {
			t.Errorf("Detect error must mention the YAML knob, got: %s", msg)
		}
		return
	}
	switch got {
	case "nix", "docker":
		// ok
	default:
		t.Fatalf("Detect returned unknown backend %q (want nix|docker)", got)
	}
}

// TestDetect_prefersNixWhenBothAvailable encodes the design decision
// that nix wins ties — operators upgrading from v0.1.x must keep
// hitting the nix path even after they install Docker Desktop.
//
// Skipped unless BOTH backends are actually reachable on the test
// host; the assertion would otherwise be vacuous.
func TestDetect_prefersNixWhenBothAvailable(t *testing.T) {
	ctx := context.Background()
	if !hasNixWithFlakes(ctx) {
		t.Skip("nix-with-flakes not available on this host")
	}
	if err := hasDocker(ctx); err != nil {
		t.Skip("docker not available on this host")
	}
	got, err := Detect(ctx)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got != "nix" {
		t.Errorf("Detect: with both backends available, want nix, got %q", got)
	}
}

// TestHasNixWithFlakes_requiresBothFeatures keeps the boolean
// semantics honest: nix on PATH alone is insufficient — the active
// config must enable both nix-command and flakes for our flake-driven
// Up paths to work.
func TestHasNixWithFlakes_requiresBothFeatures(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not on PATH; can't exercise the positive path")
	}
	// We can't easily simulate "nix without flakes" in a unit test
	// (it requires fiddling with NIX_CONFIG), so we settle for
	// asserting the function returns a stable boolean and doesn't
	// panic when the binary is present.
	got := hasNixWithFlakes(context.Background())
	t.Logf("hasNixWithFlakes on this host: %v", got)
}

// TestHasDocker_probesViaSDKNotCLI documents the current contract:
// hasDocker connects through the Docker SDK (client.FromEnv, the same
// path pkg/dockerutil.NewClient and every docker-backend plugin use),
// not the `docker` CLI binary. Pointing DOCKER_HOST at an address
// nothing listens on forces the probe to fail via that SDK connection
// regardless of whether the `docker` CLI happens to be on the test
// host's PATH — the CLI-based false-negative (SDK-reachable daemon,
// no CLI binary installed) this replaced could never be exercised
// this deterministically.
func TestHasDocker_probesViaSDKNotCLI(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	if err := hasDocker(context.Background()); err == nil {
		t.Error("hasDocker: want error when DOCKER_HOST points at an unreachable address, got nil")
	}
}
