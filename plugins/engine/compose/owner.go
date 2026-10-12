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

// foreignOwner returns where c belongs when that is a live place outside
// this worktree: its worktree-root label, or for a container made before
// the label, the compose files compose recorded, which sit in the worktree
// that started it. "" when c is this worktree's or nothing says otherwise.
func foreignOwner(c composeContainer, worktreeRoot string) string {
	if c.WorktreeRoot != "" {
		return dockerutil.OtherWorktree(c.WorktreeRoot, worktreeRoot)
	}
	worktree := filepath.Dir(worktreeRoot)
	foreign := ""
	for _, f := range strings.Split(c.ConfigFiles, ",") {
		if f == "" {
			continue
		}
		if inside(worktree, f) {
			return ""
		}
		if _, err := os.Stat(f); err == nil && foreign == "" {
			foreign = f
		}
	}
	return foreign
}

// inside reports whether path is within dir, comparing resolved paths and
// the first component of the relative path.
func inside(dir, path string) bool {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkOwner refuses to act when bough-compose-<port>, or any container
// the project already runs for service, belongs to another worktree:
// compose up would recreate it and compose stop would stop it.
func (p *Provider) checkOwner(ctx context.Context, port int, worktreeRoot, project, service string) error {
	name := fmt.Sprintf("bough-compose-%d", port)
	byName, err := p.listContainers(ctx, "name=^"+name+"$")
	if err != nil {
		return err
	}
	byProject, err := p.listContainers(ctx, "label="+labelComposeProject+"="+project, "label="+labelComposeServiceName+"="+service)
	if err != nil {
		return err
	}
	for _, c := range append(byName, byProject...) {
		if owner := foreignOwner(c, worktreeRoot); owner != "" {
			return dockerutil.WorktreeConflict(fmt.Sprintf("%s (project %s, service %s)", c.ID, project, service), owner, filepath.Dir(worktreeRoot))
		}
	}
	return nil
}

// resolveProject picks the compose project for one service: an explicit
// compose.project as is; else the project this worktree already used for
// it, from its sidecar or, when the sidecar was never written, from its
// own container's label, so an existing service keeps its project and its
// named volumes; else composeProjectName for this worktree. A recorded
// project that holds another worktree's container for the service (a
// name two clones shared before the checkout hash) is not reused.
func (p *Provider) resolveProject(ctx context.Context, worktreeRoot string, port int, file, service, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	var candidates []string
	if st, err := readSidecarState(worktreeRoot, port); err == nil && st.Project != "" && st.File == file && st.Service == service {
		candidates = append(candidates, st.Project)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read sidecar state: %w", err)
	}
	if len(candidates) == 0 {
		own, err := p.listContainers(ctx, fmt.Sprintf("name=^bough-compose-%d$", port))
		if err != nil {
			return "", err
		}
		for _, c := range own {
			if c.Project != "" && foreignOwner(c, worktreeRoot) == "" {
				candidates = append(candidates, c.Project)
			}
		}
	}
	for _, project := range candidates {
		if err := p.checkOwner(ctx, port, worktreeRoot, project, service); err == nil {
			return project, nil
		}
	}
	worktree := filepath.Dir(worktreeRoot)
	return composeProjectName(filepath.Base(worktree), file, worktree), nil
}
