package cli

import (
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
func renderWorktreeIsolation(w io.Writer) {
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

	// Outside a git work tree nothing above a container can capture git's
	// resolution, so a plain directory is correct and there is no verdict
	// to give. Saying so beats printing a failure the operator cannot act on.
	if inside, determined := insideGitWorkTree(root); determined && !inside {
		fmt.Fprintf(w, "%s Worktree isolation:\n", st.Section(termio.StatusNeutral))
		fmt.Fprintf(w, "    %s containers        %d, none inside a git repo — a host has nothing to refuse\n",
			st.Mark(termio.StatusNeutral), len(names))
		return
	}

	var refused []string
	for _, name := range names {
		if !gitwt.SelfResolvingWorkTree(filepath.Join(dir, name)) {
			refused = append(refused, name)
		}
	}
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
