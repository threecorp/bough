//go:build darwin || linux

package dockerutil

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types/container"
)

// reclaimImage is the one-shot image RemoveDatadir deletes with; the
// conformance suite already pulls it for its own datadir cleanup.
const reclaimImage = "alpine:3.20"

// RemoveDatadir is the bundled engines' Cleanup. On Linux an engine writes
// its bind-mounted datadir as its own uid (mysql 999, …), which the host
// user cannot unlink, so on a permission error the contents are deleted
// from a root container and the removal retried. Docker Desktop maps the
// uid on macOS, where the first RemoveAll already succeeds.
func RemoveDatadir(ctx context.Context, dir string) error {
	err := os.RemoveAll(dir)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if rerr := emptyAsRoot(ctx, dir); rerr != nil {
		return fmt.Errorf("%w; deleting it from a root container also failed: %w", err, rerr)
	}
	return os.RemoveAll(dir)
}

// emptyAsRoot deletes everything under dir from a root container that
// mounts only dir. It deletes rather than chowns: under rootless Docker the
// host user is the container's root, not the host uid.
func emptyAsRoot(ctx context.Context, dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
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
		Cmd:   []string{"find", "/datadir", "-mindepth", "1", "-delete"},
	}, &container.HostConfig{Binds: []string{abs + ":/datadir"}}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("create %s: %w", reclaimImage, err)
	}
	defer func() {
		_ = cli.ContainerRemove(context.WithoutCancel(ctx), resp.ID, container.RemoveOptions{Force: true})
	}()
	waitC, errC := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNextExit)
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start %s: %w", reclaimImage, err)
	}
	select {
	case w := <-waitC:
		if w.StatusCode != 0 {
			return fmt.Errorf("%s find -delete exited %d", reclaimImage, w.StatusCode)
		}
		return nil
	case err := <-errC:
		return fmt.Errorf("wait %s: %w", reclaimImage, err)
	}
}
