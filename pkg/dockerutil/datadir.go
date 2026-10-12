//go:build darwin || linux

package dockerutil

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// reclaimImage is the one-shot image RemoveDatadir deletes with; the
// conformance suite already pulls it for its own datadir cleanup.
const reclaimImage = "alpine:3.20"

// EnsureDatadir creates a bind-mounted datadir as the host user, so the
// daemon does not create it (and a missing `.local` parent) as root, which
// on Linux leaves a worktree the host user cannot remove. A worktree made
// by an earlier version can have a root-owned parent already; the daemon
// still creates the datadir there, as it used to.
func EnsureDatadir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
	}
	return nil
}

// RemoveDatadir is the bundled engines' Cleanup. On Linux an engine writes
// its bind-mounted datadir as its own uid (mysql 999, …), and a datadir the
// daemon created sits in a root-owned parent; the host user can unlink
// neither. On that permission error the datadir is deleted from a root
// container instead. Docker Desktop maps the uid on macOS, where the first
// RemoveAll already succeeds.
func RemoveDatadir(ctx context.Context, dir string) error {
	err := os.RemoveAll(dir)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if rerr := removeAsRoot(ctx, dir); rerr != nil {
		return fmt.Errorf("%w; deleting it from a root container also failed: %w", err, rerr)
	}
	if _, serr := os.Lstat(dir); !errors.Is(serr, fs.ErrNotExist) {
		return fmt.Errorf("%w; %s is still present after deleting it from a root container", err, dir)
	}
	return nil
}

// removeAsRoot deletes dir from a root container that mounts dir's parent,
// so a root-owned parent does not stop it. It deletes rather than chowns:
// under rootless Docker the host user is the container's root, not the
// host uid. A symlink is refused, since the mount would follow it.
func removeAsRoot(ctx context.Context, dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (mode %s); refusing to delete it as root", abs, fi.Mode().Type())
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return fmt.Errorf("refusing to delete %s as root", abs)
	}

	cli, err := NewClient()
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	if err := PullIfMissing(ctx, cli, reclaimImage); err != nil {
		return fmt.Errorf("pull %s: %w", reclaimImage, err)
	}
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: reclaimImage,
		User:  "0:0",
		Cmd:   []string{"rm", "-rf", "--", "/parent/" + filepath.Base(abs)},
	}, &container.HostConfig{Binds: []string{parent + ":/parent"}}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("create %s: %w", reclaimImage, err)
	}
	runErr := runToExit(ctx, cli, resp.ID)
	// The caller's ctx may already be done; the removal gets its own bound.
	const removeTimeout = 30 * time.Second
	rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
	defer cancel()
	if err := cli.ContainerRemove(rmCtx, resp.ID, container.RemoveOptions{Force: true}); err != nil {
		return errors.Join(runErr, fmt.Errorf("remove %s container %s: %w", reclaimImage, resp.ID, err))
	}
	return runErr
}

// runToExit starts a container and waits until it is no longer running.
// Waiting after the start for not-running, rather than registering
// next-exit before it, also returns for a container that already exited,
// which older Podman releases would otherwise miss.
func runToExit(ctx context.Context, cli *client.Client, id string) error {
	if err := cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return fmt.Errorf("start %s: %w", reclaimImage, err)
	}
	waitC, errC := cli.ContainerWait(ctx, id, container.WaitConditionNotRunning)
	select {
	case w := <-waitC:
		if w.StatusCode != 0 {
			return fmt.Errorf("%s rm -rf exited %d", reclaimImage, w.StatusCode)
		}
		return nil
	case err := <-errC:
		return fmt.Errorf("wait %s: %w", reclaimImage, err)
	}
}
