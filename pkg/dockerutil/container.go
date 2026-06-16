//go:build darwin || linux

package dockerutil

import (
	"context"
	"strings"

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
	return cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: false})
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
	if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: false}); err != nil {
		return false, err
	}
	return false, nil
}
