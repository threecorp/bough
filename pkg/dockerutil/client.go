//go:build darwin || linux

// Package dockerutil consolidates the Docker client / image / container /
// network / labels helpers that were duplicated verbatim across the
// mysql, postgres, redis and elasticsearch plugin docker.go files.
//
// Each plugin still owns its own engine-specific concerns (env scheme,
// readiness probe command, ulimits, persistence flags), but the generic
// "talk to dockerd" plumbing — connect, pull if missing, lookup by name,
// remove on conflict, idempotent reuse, host-side port probe, label
// schema — lives here so v0.2.x bug-fixes land in one place.
//
// The package is darwin / linux only because the plugins themselves are
// Unix-only (services-flake + Setsid). Keeping the build tag aligned
// with the plugins prevents accidental Windows linking when GoReleaser
// cross-compiles bough.
package dockerutil

import (
	"fmt"

	"github.com/docker/docker/client"
)

// NewClient connects to whichever Docker-compatible daemon the caller's
// DOCKER_HOST points at (Docker Desktop / OrbStack / Colima / rootless
// podman). API negotiation lets the SDK match the daemon's version
// without us pinning here.
//
// Callers MUST `defer cli.Close()` to release the negotiated transport.
func NewClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return cli, nil
}
