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

// RemoveDatadir removes the datadir and sibling image record idempotently.
// Engine-owned files or a root-owned parent use a root container on Linux.
// A symlinked datadir or parent is refused.
func RemoveDatadir(ctx context.Context, dir string) error {
	if dir == "" {
		return nil
	}
	dir = filepath.Clean(dir)
	// Path resolution follows a symlinked .local, so another worktree's
	// datadir would be deleted under this one's name.
	for _, p := range []string{filepath.Dir(dir), dir} {
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to delete the datadir through it", p)
		}
	}
	err := os.RemoveAll(dir)
	if err == nil {
		err = os.Remove(datadirImagePath(dir))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
	}
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if rerr := removeAsRoot(ctx, dir); rerr != nil {
		return fmt.Errorf("%w; deleting it from a root container also failed: %w", err, rerr)
	}
	for _, path := range []string{dir, datadirImagePath(dir)} {
		if _, serr := os.Lstat(path); !errors.Is(serr, fs.ErrNotExist) {
			return fmt.Errorf("%w; %s is still present after deleting it from a root container", err, path)
		}
	}
	return nil
}

// removeAsRoot deletes the datadir and record through their parent mount.
// A symlinked datadir is refused before starting a root container.
func removeAsRoot(ctx context.Context, dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(abs)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil && !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (mode %s); refusing to delete it as root", abs, fi.Mode().Type())
	}
	return runInDatadirParent(ctx, abs, []string{"rm", "-rf", "--", "/parent/" + filepath.Base(abs), "/parent/" + filepath.Base(datadirImagePath(abs))}, "cleanup")
}

// Both Cleanup and image recording need access to a legacy root-owned parent.
func runInDatadirParent(ctx context.Context, dir string, cmd []string, operation string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return fmt.Errorf("refusing to mount the parent of %s as root", abs)
	}
	cli, err := NewClient()
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()
	if err := PullIfMissing(ctx, cli, reclaimImage); err != nil {
		return fmt.Errorf("%s: pull %s: %w", operation, reclaimImage, err)
	}
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: reclaimImage,
		User:  "0:0",
		Cmd:   cmd,
	}, &container.HostConfig{Binds: []string{parent + ":/parent"}}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("%s: create %s: %w", operation, reclaimImage, err)
	}
	runErr := runToExit(ctx, cli, resp.ID, operation)
	// The caller's ctx may already be done; the removal gets its own bound.
	const removeTimeout = 30 * time.Second
	rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
	defer cancel()
	if err := cli.ContainerRemove(rmCtx, resp.ID, container.RemoveOptions{Force: true}); err != nil {
		return errors.Join(runErr, fmt.Errorf("%s: remove %s container %s: %w", operation, reclaimImage, resp.ID, err))
	}
	return runErr
}

// runToExit starts a container and waits until it is no longer running.
// Waiting after the start for not-running, rather than registering
// next-exit before it, also returns for a container that already exited,
// which older Podman releases would otherwise miss.
func runToExit(ctx context.Context, cli *client.Client, id, operation string) error {
	if err := cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return fmt.Errorf("%s: start %s: %w", operation, reclaimImage, err)
	}
	waitC, errC := cli.ContainerWait(ctx, id, container.WaitConditionNotRunning)
	select {
	case w := <-waitC:
		if w.StatusCode != 0 {
			return fmt.Errorf("%s %s exited %d", reclaimImage, operation, w.StatusCode)
		}
		return nil
	case err := <-errC:
		return fmt.Errorf("%s: wait %s: %w", operation, reclaimImage, err)
	}
}
