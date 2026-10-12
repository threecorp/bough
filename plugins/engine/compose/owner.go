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
	ID, Project, WorktreeRoot, ConfigFiles string
}

// listContainers runs `docker ps -a` with the given filters. A failure is
// an error, never "nothing there": Up and Down would otherwise act on a
// project nobody identified.
func (p *Provider) listContainers(ctx context.Context, filters ...string) ([]composeContainer, error) {
	args := []string{"ps", "-a", "--no-trunc", "--format",
		`{{.ID}}	{{.Label "` + labelComposeProject + `"}}	{{.Label "` + dockerutil.LabelWorktreeRoot + `"}}	{{.Label "` + labelComposeConfigFiles + `"}}`}
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
		f := append(strings.Split(line, "\t"), "", "", "")
		rows = append(rows, composeContainer{ID: f[0], Project: f[1], WorktreeRoot: f[2], ConfigFiles: f[3]})
	}
	return rows, nil
}

// foreignOwner returns "" only when c is provably this worktree's: its
// worktree-root label names this worktree (or a worktree that no longer
// exists, like the native engines), or, for a container made before the
// label or a dependency the override does not label, a compose file
// recorded on it is the same file as one of ownFiles (this worktree's
// compose and override files). Anything else is foreign, including a
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
// that name nothing stand alone.
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
				"between clones: `bough remove` this worktree, first deleting its own container with "+
				"`docker rm -f %s` if remove refuses, and create it again to get a project of its own)",
				dockerutil.WorktreeConflict(fmt.Sprintf("%s (project %s)", c.ID, project), owner, filepath.Dir(worktreeRoot)), name)
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
// compose.project: the running container would stay in the old one while
// the sidecar named the new one. Up refuses a recorded project that holds
// another worktree's container instead.
func (p *Provider) resolveProject(ctx context.Context, worktreeRoot string, port int, file, service, explicit string, ownFiles []string) (string, error) {
	if explicit != "" {
		// This worktree's container keeps the project it was started in;
		// a changed compose.project would leave it there while the
		// sidecar named the new one.
		own, err := p.listContainers(ctx, fmt.Sprintf("name=^bough-compose-%d$", port))
		if err != nil {
			return "", err
		}
		for _, c := range own {
			if c.Project != "" && c.Project != explicit && foreignOwner(c, worktreeRoot, ownFiles) == "" {
				return "", fmt.Errorf("compose.project is %s, but this worktree's container bough-compose-%d runs in project %s: "+
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
