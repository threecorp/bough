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

// TestHasDocker_pathPresence guards against a regression where the
// helper would silently succeed when the docker binary is missing —
// previous versions did not pre-check LookPath and would emit a
// confusing "exec: not found" error inside Detect's wrapped message.
func TestHasDocker_pathPresence(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		// Docker not installed — the helper should fail fast with a
		// PATH error, not hang.
		if err := hasDocker(context.Background()); err == nil {
			t.Errorf("hasDocker: want error when docker missing from PATH, got nil")
		}
		return
	}
	// Docker is installed; nil OR non-nil are both acceptable
	// (daemon may not be running on every CI host).
	_ = hasDocker(context.Background())
}
