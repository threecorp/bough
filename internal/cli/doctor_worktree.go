package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ikeikeikeike/bough/internal/gitwt"
	"github.com/ikeikeikeike/bough/internal/termio"
)

// maxNamedWorktrees bounds how many container names one line lists.
// Whatever is cut is SAID to be cut — a bounded list that reads as the
// whole set is how a diagnostic quietly under-reports.
const maxNamedWorktrees = 5

// renderWorktreeIsolation reports how many worktree containers a Claude
// Code host would actually accept.
//
// It exists because the failure it detects is invisible from inside
// bough: every container is provisioned correctly, the sub-repo worktrees
// are all there, and the only symptom is that the host refuses to cd into
// it — at the very end, in the host's words, in the operator's terminal.
// Printing the POPULATION (isolated of total) rather than a bare verdict
// is the point: it distinguishes "all containers are fine" from "there
// are no containers to judge", which a boolean cannot.
//
// Containers created before bough made them work trees of their own stay
// plain directories on disk (a populated dir cannot be adopted by
// `git worktree add`), so they are named here rather than silently
// migrated — recreating one is the fix, and it is the operator's call.
func renderWorktreeIsolation(ctx context.Context, w io.Writer) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	root := resolveMonorepoRoot(cwd)
	dir := worktreesDir(root)
	names, err := containerNames(dir)
	if err != nil || len(names) == 0 {
		return // nothing provisioned here; a diagnostic with no subject says nothing
	}

	fmt.Fprintln(w)
	st := termio.NewStyler(w)

	// Two different "no verdict" cases, kept apart because conflating them
	// is how a diagnostic starts lying. Undetermined means git could not be
	// RUN — every per-container probe below would fail for that same reason
	// and the report would blame the containers for it.
	inside, determined := insideGitWorkTree(root)
	switch {
	case !determined:
		fmt.Fprintf(w, "%s Worktree isolation:\n", st.Section(termio.StatusNeutral))
		fmt.Fprintf(w, "    %s containers        %d, but git could not be run here — nothing can be judged\n",
			st.Mark(termio.StatusNeutral), len(names))
		return
	case !inside:
		// Outside a git work tree nothing above a container can capture
		// git's resolution, so a plain directory is correct.
		fmt.Fprintf(w, "%s Worktree isolation:\n", st.Section(termio.StatusNeutral))
		fmt.Fprintf(w, "    %s containers        %d, none inside a git repo — a host has nothing to refuse\n",
			st.Mark(termio.StatusNeutral), len(names))
		return
	}

	refused := refusedContainers(ctx, root, dir, names)
	isolated := len(names) - len(refused)

	status := termio.StatusOK
	if len(refused) > 0 {
		status = termio.StatusWarn
	}
	fmt.Fprintf(w, "%s Worktree isolation:\n", st.Section(status))
	fmt.Fprintf(w, "    %s containers        %d of %d resolve to themselves (%s)\n",
		st.Mark(status), isolated, len(names), dir)
	if len(refused) == 0 {
		return
	}
	fmt.Fprintf(w, "    %s refused           %s\n", st.Mark(termio.StatusWarn), namedList(refused))
	fmt.Fprintf(w, "          `claude --worktree <name>` refuses these: git resolves them to %s,\n", root)
	fmt.Fprintf(w, "          so commands run there would write outside the worktree. Recreate one\n")
	fmt.Fprintf(w, "          (`bough remove <name>` then `bough create <name>`) to fix it, or start\n")
	fmt.Fprintf(w, "          the session with `cd %s/<name> && claude`.\n", dir)
}

// refusedContainers returns the container names a host would refuse.
//
// One `git worktree list --porcelain` against the monorepo root answers
// for every container bough itself created, in a single subprocess. Only
// the leftovers — a legacy plain directory, or a container that is its own
// standalone repository — need the per-directory probe, so the cost is
// 1 + misses rather than 2 per container on every `bough doctor`.
func refusedContainers(ctx context.Context, root, dir string, names []string) []string {
	runner := gitwt.NewRunner()
	registered := map[string]struct{}{}
	if wts, err := runner.List(ctx, root); err == nil {
		for _, wt := range wts {
			if !wt.Prunable {
				registered[wt.Path] = struct{}{}
			}
		}
	}
	var refused []string
	for _, name := range names {
		path := filepath.Join(dir, name)
		// git reports the symlink-resolved path; the caller holds the
		// literal one, and on stock macOS those differ for the same dir.
		resolved := path
		if real, err := filepath.EvalSymlinks(path); err == nil {
			resolved = real
		}
		if _, ok := registered[resolved]; ok {
			continue
		}
		if !runner.SelfResolvingWorkTree(ctx, path) {
			refused = append(refused, name)
		}
	}
	return refused
}

// containerNames lists the worktree containers directly under dir.
func containerNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// namedList renders at most maxNamedWorktrees names and states how many
// it left out, so the line is never mistaken for the whole set.
func namedList(names []string) string {
	if len(names) <= maxNamedWorktrees {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s (+%d more)",
		strings.Join(names[:maxNamedWorktrees], ", "), len(names)-maxNamedWorktrees)
}
