//go:build darwin || linux

package dockerutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/client"
)

func datadirImagePath(dir string) string {
	return filepath.Clean(dir) + ".image"
}

// CheckDatadirImage protects data even after its container has disappeared.
// Missing records leave older worktrees usable; an empty directory has no image.
func CheckDatadirImage(ctx context.Context, cli *client.Client, name, dir, wantImage string) error {
	_, err := os.Stat(datadirImagePath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	empty, err := datadirEmpty(dir)
	if err != nil {
		return err
	}
	if empty {
		return nil
	}
	id, err := LookupByName(ctx, cli, name)
	if err != nil {
		return err
	}
	if id != "" {
		return nil
	}
	data, err := os.ReadFile(datadirImagePath(dir))
	if err != nil {
		return fmt.Errorf("read image record: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	have := strings.TrimSpace(lines[0])
	if have == "" {
		return fmt.Errorf("image record %q is empty", datadirImagePath(dir))
	}
	if !imageSwapped(wantImage, have) {
		return nil
	}
	if len(lines) > 1 && strings.TrimSpace(lines[1]) != "" && sameImageID(ctx, cli, wantImage, strings.TrimSpace(lines[1])) {
		return nil
	}
	return &ImageMismatchError{HaveImage: have, WantImage: wantImage}
}

// An engine-owned 0700 directory cannot be proved empty by the host user.
// Keep checking its record; recovery explicitly deletes that record too.
func datadirEmpty(dir string) (bool, error) {
	f, err := os.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if errors.Is(err, fs.ErrPermission) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open data directory: %w", err)
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	if errors.Is(err, fs.ErrPermission) {
		return false, nil
	}
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read data directory: %w", err)
	}
	return false, nil
}

// RecordDatadirImage writes beside the engine-owned directory, never inside it.
// Root-owned parents from older worktrees use the same fallback as Cleanup.
func RecordDatadirImage(ctx context.Context, cli *client.Client, name, dir, imageRef string) error {
	imageID, err := writerImageID(ctx, cli, name, imageRef)
	if err != nil {
		return err
	}
	path := datadirImagePath(dir)
	err = writeImageRecord(path, imageRef, imageID)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	cmd := []string{
		"sh", "-c",
		`tmp=$(mktemp /parent/.bough-image.XXXXXX) || exit; trap 'rm -f -- "${tmp:?}"' EXIT; printf '%s\n%s\n' "$2" "$3" > "$tmp" && chmod 0644 "$tmp" && mv -fT -- "${tmp:?}" "${1:?}"`,
		"sh", "/parent/" + filepath.Base(path), imageRef, imageID,
	}
	if rerr := runInDatadirParent(ctx, dir, cmd, "writing the image record"); rerr != nil {
		return fmt.Errorf("%w; writing it from a root container also failed: %w", err, rerr)
	}
	return nil
}

// A reused container may run an older ID than its retagged ref now names;
// the record must keep the ID that actually wrote the data.
func writerImageID(ctx context.Context, cli *client.Client, name, imageRef string) (string, error) {
	id, err := LookupByName(ctx, cli, name)
	if err != nil {
		return "", err
	}
	if id != "" {
		info, err := cli.ContainerInspect(ctx, id)
		if err != nil {
			return "", fmt.Errorf("inspect container %s: %w", name, err)
		}
		if info.Image != "" {
			return info.Image, nil
		}
	}
	img, err := cli.ImageInspect(ctx, imageRef)
	if err != nil {
		return "", fmt.Errorf("inspect image %s: %w", imageRef, err)
	}
	return img.ID, nil
}

// Rename keeps an interrupted write from publishing a partial record.
func writeImageRecord(path, imageRef, imageID string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".bough-image-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(imageRef + "\n" + imageID + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
