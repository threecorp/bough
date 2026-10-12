//go:build darwin || linux

package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ikeikeikeike/bough/pkg/dockerutil"
)

// Labels docker compose stamps on every container it creates.
const (
	labelComposeProject     = "com.docker.compose.project"
	labelComposeServiceName = "com.docker.compose.service"
	labelComposeConfigFiles = "com.docker.compose.project.config_files"
)

// composeContainer is one row of `docker ps` for the owner check.
type composeContainer struct {
	ID, Project, WorktreeRoot, ConfigFiles, State string
}

// listContainers runs `docker ps -a` with the given filters. A failure is
// an error, never "nothing there": Up and Down would otherwise act on a
// project nobody identified.
func (p *Provider) listContainers(ctx context.Context, filters ...string) ([]composeContainer, error) {
	args := []string{"ps", "-a", "--no-trunc", "--format",
		`{{.ID}}	{{.Label "` + labelComposeProject + `"}}	{{.Label "` + dockerutil.LabelWorktreeRoot + `"}}	{{.Label "` + labelComposeConfigFiles + `"}}	{{.State}}`}
	for _, f := range filters {
		args = append(args, "--filter", f)
	}
	lctx, cancel := context.WithTimeout(ctx, p.cmdWait)
	defer cancel()
	cmd := exec.CommandContext(lctx, "docker", args...)
	cmd.WaitDelay = p.pipeWait
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w\n%s", err, stderr.String())
	}
	var rows []composeContainer
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := append(strings.Split(line, "\t"), "", "", "", "")
		rows = append(rows, composeContainer{ID: f[0], Project: f[1], WorktreeRoot: f[2], ConfigFiles: f[3], State: f[4]})
	}
	return rows, nil
}

// foreignOwner returns "" only when c is provably this worktree's: its
// worktree-root label names this worktree (or a worktree that no longer
// exists, like the native engines), or, for a container made before the
// label or a dependency the override does not label, a compose file
// recorded on it is the same file as one of ownFiles: this worktree's
// override for the port, which no other worktree uses, unlike a compose
// file several worktrees may share. Anything else is foreign, including a
// legacy container whose files are gone, since compose up would recreate
// it and compose stop would stop it.
func foreignOwner(c composeContainer, worktreeRoot string, ownFiles []string) string {
	if c.WorktreeRoot != "" {
		return dockerutil.OtherWorktree(c.WorktreeRoot, worktreeRoot)
	}
	for _, f := range recordedFiles(c.ConfigFiles) {
		if sameAsAny(f, ownFiles) {
			return ""
		}
	}
	if c.ConfigFiles != "" {
		return "the worktree of " + c.ConfigFiles
	}
	return "an unknown owner (no bough label, no compose files recorded)"
}

// recordedFiles splits compose's comma-joined config file label. A path
// may itself hold a comma, so at each position the longest run of
// comma-separated pieces that names an existing file is one path; pieces
// that name nothing stand alone. The label cannot be split exactly: a
// deleted foreign file whose directory names were built to end in ","
// plus this worktree's override path would still read as ours.
func recordedFiles(label string) []string {
	if label == "" {
		return nil
	}
	pieces := strings.Split(label, ",")
	var files []string
	for i := 0; i < len(pieces); {
		end := i + 1
		for j := len(pieces); j > i+1; j-- {
			if _, err := os.Stat(strings.Join(pieces[i:j], ",")); err == nil {
				end = j
				break
			}
		}
		files = append(files, strings.Join(pieces[i:end], ","))
		i = end
	}
	return files
}

// sameAsAny reports whether path is the same file as one of files: the
// comparison is by identity, so a symlinked alias or another letter case
// on a case-insensitive filesystem still matches, and a different file
// never does.
func sameAsAny(path string, files []string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	for _, f := range files {
		if other, err := os.Stat(f); err == nil && os.SameFile(fi, other) {
			return true
		}
	}
	return false
}

// checkOwner refuses to act when bough-compose-<port>, or a container of
// project, is not provably this worktree's. Up passes service "" to check
// every service, since compose up also starts and may recreate the
// services the target depends on; Down passes its one service, which is
// all compose stop and rm touch. A failed docker call is returned as is.
func (p *Provider) checkOwner(ctx context.Context, port int, worktreeRoot, project, service string, ownFiles []string) error {
	name := fmt.Sprintf("bough-compose-%d", port)
	byName, err := p.listContainers(ctx, "name=^"+name+"$")
	if err != nil {
		return err
	}
	filters := []string{"label=" + labelComposeProject + "=" + project}
	if service != "" {
		filters = append(filters, "label="+labelComposeServiceName+"="+service)
	}
	byProject, err := p.listContainers(ctx, filters...)
	if err != nil {
		return err
	}
	for _, c := range append(byName, byProject...) {
		if owner := foreignOwner(c, worktreeRoot, ownFiles); owner != "" {
			return fmt.Errorf("%w (a project recorded before project names carried a checkout hash can be shared "+
				"between clones: `bough remove` this worktree and create it again to get a project of its own)",
				dockerutil.WorktreeConflict(fmt.Sprintf("%s (project %s)", c.ID, project), owner, filepath.Dir(worktreeRoot)))
		}
	}
	return nil
}

// resolveProject picks the compose project for one service: an explicit
// compose.project as is; else the project this worktree already used for
// it, from its sidecar or, when the sidecar was never written, from its
// own container's label, so an existing service keeps its project and its
// named volumes; else composeProjectName for this worktree. A recorded
// project is never swapped for another, by bough or by a changed
// compose.project while this worktree's container exists: the container
// would stay in the old one while the sidecar named the new one. Up
// refuses a recorded project that holds another worktree's container.
func (p *Provider) resolveProject(ctx context.Context, worktreeRoot string, port int, file, service, explicit string, ownFiles []string) (string, error) {
	if explicit != "" {
		// This worktree's container keeps the project it was created in,
		// whatever its state: reusing a running (or restarting, or paused)
		// one would leave it there while the sidecar named the new project,
		// and recreating a stopped one could fail midway with the old
		// sidecar still naming the old project. `bough remove` clears it.
		own, err := p.listContainers(ctx, fmt.Sprintf("name=^bough-compose-%d$", port))
		if err != nil {
			return "", err
		}
		for _, c := range own {
			if c.Project != "" && c.Project != explicit && foreignOwner(c, worktreeRoot, ownFiles) == "" {
				return "", fmt.Errorf("compose.project is %s, but this worktree's container bough-compose-%d is in project %s: "+
					"`bough remove` this worktree and create it again to move it", explicit, port, c.Project)
			}
		}
		return explicit, nil
	}
	st, err := readSidecarState(worktreeRoot, port)
	switch {
	case err == nil && st.Project != "" && st.File == file && st.Service == service:
		return st.Project, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("read sidecar state: %w", err)
	}
	own, err := p.listContainers(ctx, fmt.Sprintf("name=^bough-compose-%d$", port))
	if err != nil {
		return "", err
	}
	for _, c := range own {
		if c.Project != "" && foreignOwner(c, worktreeRoot, ownFiles) == "" {
			return c.Project, nil
		}
	}
	worktree := filepath.Dir(worktreeRoot)
	return composeProjectName(filepath.Base(worktree), file, worktree), nil
}

// removeOwnContainer is Down without a sidecar: an Up that failed after
// starting the container but before writing the sidecar left nothing for
// compose stop to target. A container under this port's name that is
// provably this worktree's is removed directly; anything else is left.
func (p *Provider) removeOwnContainer(ctx context.Context, port int, worktreeRoot string) error {
	name := fmt.Sprintf("bough-compose-%d", port)
	rows, err := p.listContainers(ctx, "name=^"+name+"$")
	if err != nil {
		return err
	}
	for _, c := range rows {
		if foreignOwner(c, worktreeRoot, []string{overrideFilePath(worktreeRoot, port)}) != "" {
			continue
		}
		// By the ID that was checked, not the name: another create may have
		// reused the name in between.
		rctx, cancel := context.WithTimeout(ctx, p.cmdWait)
		cmd := exec.CommandContext(rctx, "docker", "rm", "-f", c.ID)
		cmd.WaitDelay = p.pipeWait
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil && !errors.Is(err, exec.ErrWaitDelay) && !strings.Contains(string(out), "No such container") {
			return fmt.Errorf("docker rm -f %s (%s): %w\n%s", c.ID, name, err, out)
		}
		if c.Project != "" {
			if err := p.removeEmptyProjectNetworks(ctx, c.Project); err != nil {
				return err
			}
		}
	}
	return nil
}

// removeEmptyProjectNetworks removes the networks of project once it holds
// no container. Down stops one service and never runs compose down, which
// would stop the siblings too, so without this every removed worktree
// left its project network behind until Docker ran out of address pools.
// A network some other container still names, by ID or by name (a created
// but never started container records only the name), stays, stopped
// containers included. A failure the daemon reports as "in use" or "gone"
// is the outcome this wanted; any other failure is returned.
//
// Two leftovers remain, and `docker network prune` clears them: a network
// compose created for an Up that failed before any container existed (a
// missing external volume, say), or one whose cleanup failed after the
// container and sidecar were already gone, is not found again. And a
// container created on the network between the check and the removal is
// not seen; it fails to start afterwards.
func (p *Provider) removeEmptyProjectNetworks(ctx context.Context, project string) error {
	left, err := p.listContainers(ctx, "label="+labelComposeProject+"="+project)
	if err != nil || len(left) > 0 {
		return err
	}
	out, err := p.docker(ctx, "network", "ls", "--no-trunc", "--format", "{{.ID}}\t{{.Name}}", "--filter", "label="+labelComposeProject+"="+project)
	if err != nil {
		return fmt.Errorf("docker network ls: %w\n%s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id, name, _ := strings.Cut(line, "\t")
		if id == "" {
			continue
		}
		inUse := false
		for _, ref := range []string{id, name} {
			if ref == "" {
				continue
			}
			users, err := p.listContainers(ctx, "network="+ref)
			if err != nil {
				return err
			}
			inUse = inUse || len(users) > 0
		}
		if inUse {
			continue
		}
		msg, err := p.docker(ctx, "network", "rm", id)
		if err == nil || strings.Contains(msg, "active endpoints") || networkGone(msg) {
			continue
		}
		if imsg, ierr := p.docker(ctx, "network", "inspect", id); ierr != nil && networkGone(imsg) {
			continue
		}
		return fmt.Errorf("docker network rm %s (%s): %w\n%s", id, name, err, msg)
	}
	return nil
}

// networkGone reports whether a docker message says the network no
// longer exists.
func networkGone(msg string) bool {
	return strings.Contains(msg, "No such network") || strings.Contains(msg, "not found") || strings.Contains(msg, "unknown network")
}

// docker runs one docker CLI command bounded by cmdWait and returns its
// combined output.
func (p *Provider) docker(ctx context.Context, args ...string) (string, error) {
	dctx, cancel := context.WithTimeout(ctx, p.cmdWait)
	defer cancel()
	cmd := exec.CommandContext(dctx, "docker", args...)
	cmd.WaitDelay = p.pipeWait
	out, err := cmd.CombinedOutput()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return string(out), err
}
