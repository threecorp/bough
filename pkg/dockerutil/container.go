//go:build darwin || linux

package dockerutil

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// RemoveIfExists is the idempotency helper for each plugin's docker Up:
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
// hint: a container named `name` belongs to one engine instance (the
// bough port allocator keeps ports unique within one registry; another
// checkout's container of the same name is refused by UpOrReuse and
// CheckWorktree), so an actually-running one is sufficient evidence that
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

// ImageMismatchError lets callers name the bind-mounted datadir in the remedy.
type ImageMismatchError struct {
	ContainerName string
	HaveImage     string
	WantImage     string
}

func (e *ImageMismatchError) Error() string {
	if e.HaveImage == "" {
		return fmt.Sprintf("data directory has no image record and no container, so bough cannot tell which image wrote it; "+
			"bough does not start %s on data another image may have written", e.WantImage)
	}
	if e.ContainerName == "" {
		return fmt.Sprintf("data directory was written by %s, not the %s this config asks for; "+
			"bough does not start another image on the data directory the first one wrote", e.HaveImage, e.WantImage)
	}
	return fmt.Sprintf("container %s was created from %s, not the %s this config asks for; "+
		"bough does not start another image on the data directory the first one wrote",
		e.ContainerName, e.HaveImage, e.WantImage)
}

// WithDatadir names the data to clear, plus the container if present.
// Keeping the directory permits reuse under a root-owned parent on Linux.
// Recovery records the wanted ref rather than deleting the record: an
// engine-owned 0700 datadir cannot be proved empty by the host user.
func (e *ImageMismatchError) WithDatadir(datadir string) error {
	absolute, err := filepath.Abs(datadir)
	if err != nil {
		return fmt.Errorf("%w; resolve data directory: %w", e, err)
	}
	record := fmt.Sprintf("`printf '%%s\\n' %s > %s` (in a root shell if the data directory's parent is root-owned)",
		shellQuote(e.WantImage), shellQuote(datadirImagePath(absolute)))
	removeContainer := ""
	if e.ContainerName != "" {
		removeContainer = fmt.Sprintf("first run `docker rm -fv %s`, then ", e.ContainerName)
	}
	keepData := ""
	if e.HaveImage == "" {
		keepData = fmt.Sprintf("if %s wrote this data, record that with %s and resume the same worktree; otherwise ", e.WantImage, record)
	}
	return fmt.Errorf("%w; %sto start over with an empty data directory while keeping the worktree, "+
		"%sempty the data directory %q (delete everything inside it, keep the directory itself; all data in that directory is lost; "+
		"on Linux its files belong to the container's user, so empty it as root), "+
		"record the wanted image with %s, "+
		"and resume the same worktree the same way it was created (the WorktreeCreate hook, or `bough create` with the same worktree name); or run `bough remove` to discard the whole worktree and create it again",
		e, keepData, removeContainer, absolute, record)
}

// shellQuote makes a remedy command safe to paste: only ' is special inside '…'.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// UpOrReuse implements the --resume idempotency contract for Up.
//
// Returns skip=true if a container with `name` is already running the
// image in wantImage, so the caller can early-return without
// recreating. A stopped container, or one whose different owner worktree
// is gone, is removed after the image check and returns skip=false for a
// fresh create + start; the latter must not serve another datadir.
//
// A container another worktree created (LabelWorktreeRoot names a live
// directory other than wantWorktree) is an error too, running or stopped:
// reusing it would point this worktree at that one's database, and
// removing it would delete that database's container. wantWorktree == ""
// skips the check. A container without the label (made by an older bough)
// is checked by its datadir bind instead (containerWorktree).
//
// A container created from another image — running or stopped — is an
// error rather than a reuse or a recreate: engines[].version picks the
// image, and either path would leave the new version serving, or about to
// serve, a data directory the old one wrote, while .bough.yaml names the
// new one. Callers can use ImageMismatchError.WithDatadir to name both
// the container and its data directory in the remedy. wantImage == ""
// skips the comparison, for a caller whose image is not bough's to choose.
//
// Mirrors threecorp scripts/worktree-create.sh:46-50 — "skip if
// worktree already exists" but at the container layer.
func UpOrReuse(ctx context.Context, cli *client.Client, name, wantImage, wantWorktree string) (bool, error) {
	id, err := LookupByName(ctx, cli, name)
	if err != nil {
		return false, err
	}
	if id == "" {
		return false, nil
	}
	// A failed inspect leaves the owner and image unchecked, so it must not
	// fall through to the force remove below; only "already gone" may.
	info, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	owner := containerWorktree(info.Config, info.HostConfig)
	if other := otherOwner(owner, wantWorktree); other != "" {
		return false, WorktreeConflict(name, other, filepath.Dir(wantWorktree))
	}
	have := configImage(info.Config)
	if imageSwapped(wantImage, have) && !sameImageID(ctx, cli, wantImage, info.Image) {
		return false, &ImageMismatchError{ContainerName: name, HaveImage: have, WantImage: wantImage}
	}
	sameOwner := owner == "" || wantWorktree == "" || samePath(owner, filepath.Dir(wantWorktree))
	if sameOwner && info.State != nil && info.State.Running {
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
	// An image bough chose may declare a VOLUME its bind mount does not
	// cover (postgres 18+ moved it to the parent of PGDATA), and that
	// anonymous volume outlives the container unless removed with it. A
	// compose service's image is the operator's, and a same-named
	// container bough did not label is someone else's: both keep theirs.
	removeVolumes := wantImage != "" && ownedByBough(info.Config)
	if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: removeVolumes}); err != nil && !errdefs.IsNotFound(err) {
		return false, err
	}
	return false, nil
}

// CheckWorktree is Down's half of the UpOrReuse owner check: stopping a
// container another checkout's worktree created would take that
// worktree's database down, so it is an error instead, labelled or not. A
// container that vanished meanwhile is not; Down then has nothing to stop.
func CheckWorktree(ctx context.Context, cli *client.Client, id, name, wantWorktree string) error {
	info, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return err
	}
	if owner := otherOwner(containerWorktree(info.Config, info.HostConfig), wantWorktree); owner != "" {
		return WorktreeConflict(name, owner, filepath.Dir(wantWorktree))
	}
	return nil
}

// containerWorktree returns the worktree dir a container belongs to: the
// parent of its LabelWorktreeRoot (an engine-provider repo inside the
// worktree), or for a container made before the label, the worktree of
// its <worktree>/.local/<kind>-data bind. "" when neither says.
func containerWorktree(cfg *container.Config, hostCfg *container.HostConfig) string {
	if cfg != nil && cfg.Labels[LabelWorktreeRoot] != "" {
		return filepath.Dir(cfg.Labels[LabelWorktreeRoot])
	}
	if hostCfg == nil {
		return ""
	}
	for _, bind := range hostCfg.Binds {
		src, _, _ := strings.Cut(bind, ":")
		if filepath.Base(filepath.Dir(src)) == ".local" && strings.HasSuffix(filepath.Base(src), "-data") {
			return filepath.Dir(filepath.Dir(src))
		}
	}
	return ""
}

// OtherWorktree returns the worktree dir of have, a container's
// LabelWorktreeRoot, when it is a live worktree other than want's. Both
// are engine-provider repo paths (UpReq.WorktreeRoot); the worktree is
// their parent, which also holds the datadir, so a deleted repo dir does
// not free the database. A gone owner is not a conflict: UpOrReuse removes
// its container after the image check, and Down may stop it. Names are
// bough-<engine>-<port> and the port is a hash of the worktree name, so a
// second clone of the same monorepo with a same-named worktree lands on
// the same name.
func OtherWorktree(have, want string) string {
	if have == "" {
		return ""
	}
	return otherOwner(filepath.Dir(have), want)
}

// otherOwner returns owner when it names a live worktree dir other than
// the parent of want. An unknown owner, an empty want, or an owner dir
// that no longer exists is not a conflict. UpOrReuse separately prevents
// reuse for a different gone owner; Down may stop its container.
func otherOwner(owner, want string) string {
	if owner == "" || want == "" || samePath(owner, filepath.Dir(want)) {
		return ""
	}
	if _, err := os.Stat(owner); errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	return owner
}

// samePath compares two spellings of a directory by the file they open,
// so a worktree reached through /tmp and /private/tmp, or typed in another
// letter case on a case-insensitive filesystem, is still one worktree.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// WorktreeConflict is the refusal for a container OtherWorktree flagged.
func WorktreeConflict(name, have, want string) error {
	return fmt.Errorf("container %s belongs to %s, not %s: both were given the same port "+
		"(another checkout with a worktree of the same name, or another monorepo sharing these port ranges). "+
		"bough leaves that container alone — name this worktree differently, or give this checkout its own port ranges",
		name, have, want)
}

// RemoveOwned force-removes a container, taking its anonymous volumes
// with it only when bough created it. The engines' Down use it so a
// postgres 18+ worktree does not leave one dangling volume behind.
func RemoveOwned(ctx context.Context, cli *client.Client, id string) error {
	owned := false
	if info, err := cli.ContainerInspect(ctx, id); err == nil {
		owned = ownedByBough(info.Config)
	}
	return cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: owned})
}

// ownedByBough reports whether a container carries the label every bough
// plugin stamps on what it creates (Labels), so deleting its anonymous
// volumes cannot reach a same-named container someone else made.
func ownedByBough(cfg *container.Config) bool {
	return cfg != nil && cfg.Labels[LabelManaged] == "true"
}

func configImage(cfg *container.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Image
}

// sameImageID reports whether ref resolves in the local image store to the
// image a container was created from: the same image spelled another way
// (with the docker.io/library/ prefix, or by digest). An image not pulled
// yet cannot be compared and counts as different.
func sameImageID(ctx context.Context, cli *client.Client, ref, containerImageID string) bool {
	if containerImageID == "" {
		return false
	}
	img, err := cli.ImageInspect(ctx, ref)
	return err == nil && img.ID == containerImageID
}

// imageSwapped reports whether a running container's image ref differs
// from the one the caller resolved. An empty side means the comparison
// cannot be made — no wanted ref, or a daemon that reported no Config —
// and is never a swap, so an unknown never blocks a reuse that used to
// work.
func imageSwapped(want, have string) bool {
	return want != "" && have != "" && want != have
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
		// Only the bundled engines call this, so any anonymous volume is
		// one their image declared — see UpOrReuse.
		_ = cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true, RemoveVolumes: true})
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
