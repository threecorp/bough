package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ikeikeikeike/bough/internal/gitwt"
	"github.com/spf13/cobra"
)

func newRepairCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Convert legacy worktree containers into work trees of their own, in place",
		Long: `repair walks the monorepo's worktrees/ (or the pre-v0.11 hidden
.worktrees/) and converts every container that git still resolves to the
monorepo root — the shape an isolating Claude Code host refuses, clearing
the session's worktree binding — into a detached work tree of its own.

The conversion is in place: nothing inside the container is moved, deleted
or checked out over. The worktree admin entry is created at a throwaway
path against a pinned empty-tree commit, only its .git link moves into the
container, and ` + "`git worktree repair`" + ` re-points the record.

` + "`bough create`" + ` (the WorktreeCreate hook) heals a legacy container on its
own the next time it fires; repair covers a whole fleet in one pass, before
any session resumes into a container the host would refuse.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			monorepoRoot, _, err := loadConfigAndRoot(cmd, "")
			if err != nil {
				return err
			}
			return runRepair(cmd.Context(), cmd.ErrOrStderr(), monorepoRoot, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be converted without changing anything")
	return cmd
}

// runRepair applies gitwt.RepairInPlace to every container that does not
// resolve to itself. Failures are per-container: one broken container must
// not stop the rest of the fleet from being healed, so they are reported,
// counted, and turned into a non-zero exit at the end.
func runRepair(ctx context.Context, stderr io.Writer, monorepoRoot string, dryRun bool) error {
	dir := worktreesDir(monorepoRoot)
	names, err := containerNames(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(stderr, "[repair] %s does not exist — nothing to do\n", dir)
			return nil
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}
	if len(names) == 0 {
		fmt.Fprintf(stderr, "[repair] no containers under %s — nothing to do\n", dir)
		return nil
	}
	// Same two no-verdict cases doctor keeps apart: git being unrunnable
	// must not be reported as "everything already isolated", and outside a
	// repo a plain directory is the correct shape, not a broken one.
	inside, determined := insideGitWorkTree(monorepoRoot)
	if !determined {
		return fmt.Errorf("repair: git could not be run at %s — nothing can be judged", monorepoRoot)
	}
	if !inside {
		fmt.Fprintf(stderr, "[repair] %s is not inside a git repo — a host has nothing to refuse, containers stay plain dirs\n", monorepoRoot)
		return nil
	}

	runner := gitwt.NewRunner()
	var ok, fixed, failed int
	for _, name := range names {
		path := filepath.Join(dir, name)
		switch {
		case runner.SelfResolvingWorkTree(ctx, path):
			ok++
		case dryRun:
			fmt.Fprintf(stderr, "[repair] would convert %s\n", name)
			fixed++
		default:
			if err := runner.RepairInPlace(ctx, monorepoRoot, path); err != nil {
				fmt.Fprintf(stderr, "[repair] FAILED %s: %v\n", name, err)
				failed++
				continue
			}
			fmt.Fprintf(stderr, "[repair] converted %s\n", name)
			fixed++
		}
	}
	if dryRun {
		fmt.Fprintf(stderr, "[repair] already isolated: %d / would convert: %d (re-run without --dry-run)\n", ok, fixed)
		return nil
	}
	fmt.Fprintf(stderr, "[repair] already isolated: %d / converted: %d / failed: %d\n", ok, fixed, failed)
	if failed > 0 {
		return fmt.Errorf("repair: %d container(s) could not be converted", failed)
	}
	return nil
}
