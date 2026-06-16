//go:build darwin || linux

package dockerutil

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// PullIfMissing implements the if_not_present pull policy. The Docker
// API's ImagePull is a stream we MUST drain before the layer download
// is reflected on the daemon — discarding the body is correct because
// the per-plugin caller only cares about the final "image cached" side
// effect, not the JSON-encoded progress events.
//
// Returns nil on success, including the "image already cached" fast
// path (ImageInspect short-circuit), so warm cold-starts skip the
// network round-trip.
func PullIfMissing(ctx context.Context, cli *client.Client, ref string) error {
	if _, err := cli.ImageInspect(ctx, ref); err == nil {
		return nil
	}
	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("drain pull stream: %w", err)
	}
	return nil
}
