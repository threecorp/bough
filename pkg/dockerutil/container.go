//go:build darwin || linux

package dockerutil

import (
	"context"
	"fmt"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// LookupByName returns the container ID for a container whose primary
// name exactly equals `name`, or empty string if none exists.
//
// The `^/<name>$` filter is anchored because Docker name filters are
// substring-matched by default — without the anchors `bough-mysql-1`
// would match `bough-mysql-10` and the post-filter loop would still
// have to confirm the exact match. The loop stays as a defensive
// second pass in case the daemon's filter semantics change.
func LookupByName(ctx context.Context, cli *client.Client, name string) (string, error) {
	args := filters.NewArgs()
	args.Add("name", "^/"+name+"$")
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return "", err
	}
	for _, c := range list {
		for _, n := range c.Names {
			if strings.TrimPrefix(n, "/") == name {
				return c.ID, nil
			}
		}
	}
	return "", nil
}

// RemoveIfExists is the idempotency helper for the per-plugin dockerUp:
// if a previous run left a stopped (or running) container with the same
// name we tear it down so ContainerCreate does not collide. Returns nil
// when nothing is there to remove — the no-op makes the call safe in
// cleanup paths.
func RemoveIfExists(ctx context.Context, cli *client.Client, name string) error {
	id, err := LookupByName(ctx, cli, name)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	// Mirrors UpOrReuse's tolerance: the container LookupByName just
	// found can vanish before this call reaches the daemon (a
	// concurrent retry, a parallel remove, a manual `docker rm`) —
	// that race lands on the same "nothing there" state the id == ""
	// branch above already treats as success.
	if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: false}); err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	return nil
}

// IsBackendRunning is the cheap self-detection every engine plugin's
// Down/ReadyCheck uses when neither RPC carries an explicit backend
// hint: a container named `name` is uniquely owned by one engine
// instance (the bough port allocator guarantees worktree-scoped
// uniqueness), so an actually-running one is sufficient evidence that
// Up went via the Docker path.
//
// A stopped/leftover container must not count as "docker backend in
// use" — LookupByName lists with All:true, so a stale, already-
// stopped container from a prior run would otherwise make the caller
// take the docker teardown path (stop+remove the irrelevant
// container, report success) while the real engine for this
// worktree/port keeps running untouched, and
// a subsequent Cleanup() would then rm -rf its datadir out from under
// it.
//
// Returns false on any Docker error, so a caller with another backend
// registered falls through to it rather than reporting this one — that
// keeps `bough remove` working on a machine where Docker was
// uninstalled between create and remove.
func IsBackendRunning(ctx context.Context, cli *client.Client, name string) bool {
	id, err := LookupByName(ctx, cli, name)
	if err != nil || id == "" {
		return false
	}
	info, err := cli.ContainerInspect(ctx, id)
	if err != nil || info.State == nil {
		return false
	}
	return info.State.Running
}

// UpOrReuse implements the --resume idempotency contract for Up.
//
// Returns skip=true if a container with `name` is already running, so
// the caller can early-return without recreating. If the container
// exists but is stopped (a previous Up partially failed), the stale
// container is removed and skip=false is returned so the caller
// proceeds with a fresh create + start.
//
// Mirrors threecorp scripts/worktree-create.sh:46-50 — "skip if
// worktree already exists" but at the container layer.
func UpOrReuse(ctx context.Context, cli *client.Client, name string) (bool, error) {
	id, err := LookupByName(ctx, cli, name)
	if err != nil {
		return false, err
	}
	if id == "" {
		return false, nil
	}
	if info, ierr := cli.ContainerInspect(ctx, id); ierr == nil && info.State != nil && info.State.Running {
		return true, nil
	}
	// The container LookupByName just found stopped can vanish before
	// this call reaches the daemon — a concurrent `bough create` retry
	// for the same worktree (the exact retry-heavy pattern Up's own
	// callers are built around), a parallel `bough remove`, or an
	// operator's manual `docker rm`. That race lands on the identical
	// "nothing there" state the id == "" branch above already treats
	// as success, so a NotFound here must not fail Up — only a genuine
	// remove error (permissions, daemon down, in-use) should.
	if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: false}); err != nil && !errdefs.IsNotFound(err) {
		return false, err
	}
	return false, nil
}

// StartOrCleanup starts a just-created container; on failure it
// garbage-collects the stopped container (so a retry is not blocked
// by a stale name) and, when isPortConflictError recognizes the
// failure as a taken host port, rewraps it with an actionable `docker
// ps --filter publish=<port>` hint — macOS Docker Desktop's vpnkit
// makes the host port look free to net.Listen, so plugins cannot
// pre-check it reliably.
func StartOrCleanup(ctx context.Context, cli *client.Client, containerID, engineName string, port int) error {
	if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		_ = cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true, RemoveVolumes: false})
		if isPortConflictError(err) {
			return fmt.Errorf("%s docker: host port %d is already published by another container — `docker ps --filter publish=%d` to find it; raw: %w", engineName, port, port, err)
		}
		return fmt.Errorf("%s docker: start %s: %w", engineName, containerID, err)
	}
	return nil
}

// isPortConflictError recognizes both real daemon error shapes for a
// host port that is already taken:
//
//   - "port is already allocated" — another Docker container holds it.
//   - "...bind: address already in use" — a plain host-level listener
//     holds it (or a Docker container reached through a code path that
//     phrases the same OS-level bind failure differently).
//
// Matching only the first silently fell through to a generic,
// non-actionable error for exactly the case the hint exists for.
func isPortConflictError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "port is already allocated") || strings.Contains(msg, "address already in use")
}
