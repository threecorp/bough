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
// label or a dependency the override does not label, the compose files
// compose recorded on it lie in this worktree. Anything else is foreign,
// including a legacy container whose files are gone, since compose up
// would recreate it and compose stop would stop it.
func foreignOwner(c composeContainer, worktreeRoot string) string {
	if c.WorktreeRoot != "" {
		return dockerutil.OtherWorktree(c.WorktreeRoot, worktreeRoot)
	}
	if recordsWorktree(c.ConfigFiles, filepath.Dir(worktreeRoot)) {
		return ""
	}
	if c.ConfigFiles != "" {
		return "the worktree of " + c.ConfigFiles
	}
	return "an unknown owner (no bough label, no compose files recorded)"
}

// recordsWorktree reports whether files, compose's comma-joined config
// file label, starts a path inside worktree. The label is not split: a
// worktree name may hold a comma. Case is ignored, since compose records
// the path as the caller typed it on a case-insensitive filesystem.
func recordsWorktree(files, worktree string) bool {
	files = strings.ToLower(files)
	for _, dir := range []string{worktree, resolvePrefix(worktree)} {
		prefix := strings.ToLower(dir) + string(filepath.Separator)
		if strings.HasPrefix(files, prefix) || strings.Contains(files, ","+prefix) {
			return true
		}
	}
	return false
}

// resolvePrefix resolves symlinks in the longest existing prefix of path
// and appends the rest unchanged.
func resolvePrefix(path string) string {
	rest := ""
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(real, rest)
		}
		if filepath.Dir(p) == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

// checkOwner refuses to act when bough-compose-<port>, or any container
// of project (every service, since compose up also starts and may
// recreate the services the target depends on), is not provably this
// worktree's. A failed docker call is returned as is.
func (p *Provider) checkOwner(ctx context.Context, port int, worktreeRoot, project string) error {
	name := fmt.Sprintf("bough-compose-%d", port)
	byName, err := p.listContainers(ctx, "name=^"+name+"$")
	if err != nil {
		return err
	}
	byProject, err := p.listContainers(ctx, "label="+labelComposeProject+"="+project)
	if err != nil {
		return err
	}
	for _, c := range append(byName, byProject...) {
		if owner := foreignOwner(c, worktreeRoot); owner != "" {
			return fmt.Errorf("%w (a project recorded before project names carried a checkout hash can be shared "+
				"between clones: `bough remove` this worktree and create it again to get its own project)",
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
// project is never swapped for another: the running container would stay
// in the old one while the sidecar named the new one. Up refuses a
// recorded project that holds another worktree's container instead.
func (p *Provider) resolveProject(ctx context.Context, worktreeRoot string, port int, file, service, explicit string) (string, error) {
	if explicit != "" {
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
		if c.Project != "" && foreignOwner(c, worktreeRoot) == "" {
			return c.Project, nil
		}
	}
	worktree := filepath.Dir(worktreeRoot)
	return composeProjectName(filepath.Base(worktree), file, worktree), nil
}
