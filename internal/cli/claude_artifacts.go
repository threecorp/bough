package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	bough "github.com/ikeikeikeike/bough"
	"github.com/ikeikeikeike/bough/pkg/procutil"
)

// artifactKind describes one kind of Claude Code artifact bough installs from
// its embedded assets. skills and commands differ only in where they live and
// what an entry looks like on disk, so the install / uninstall / list bodies
// are written once against this description rather than twice per kind.
//
// hooks are deliberately NOT modelled here: they live inside settings.json
// (a JSON round-trip that must preserve hand-edited entries), not as free
// files, which is why internal/hooks owns them.
type artifactKind struct {
	name     string // "skill" / "command" — the subcommand name
	subdir   string // subtree inside bough.Assets AND under .claude/
	entryIs  string // human noun for one entry, used in output
	dirEntry bool   // true = one entry is a directory (skills), false = one .md file
}

var (
	skillKind   = artifactKind{name: "skill", subdir: "skills", entryIs: "skill", dirEntry: true}
	commandKind = artifactKind{name: "command", subdir: "commands", entryIs: "command", dirEntry: false}
)

// newSkillCmd / newCommandCmd are the `bough claude skill|command` surfaces.
// They mirror `bough claude hook`'s verb set (install / uninstall / list) so an
// operator who knows one knows the others; `doctor` stays a single top-level
// report rather than a per-kind duplicate.
func newSkillCmd() *cobra.Command { return newArtifactCmd(skillKind) }

func newCommandCmd() *cobra.Command { return newArtifactCmd(commandKind) }

func newArtifactCmd(k artifactKind) *cobra.Command {
	cmd := &cobra.Command{
		Use:   k.name,
		Short: fmt.Sprintf("Manage the bough %ss installed under .claude/%s", k.entryIs, k.subdir),
		Long: fmt.Sprintf(`bough claude %s installs the %ss bough ships (the same content the
Claude Code plugin publishes) into .claude/%s.

This is the CLI path, for operators who want bough's %ss without
installing the plugin — or who want them at a scope the plugin does
not offer. The content is embedded in the binary, so the installed
copy always matches the bough version you are running.

Only bough's own entries are managed: uninstall removes the names
bough ships and nothing else, so anything you authored alongside
them stays put.

install is the exception, and deliberately so: it overwrites every
name bough ships, which is how you pick up a new version's content.
bough cannot tell its own older copy from a file of yours that
happens to share the name (it ships names as generic as list.md), so
it reports each entry as new or replaced rather than guessing.`, k.name, k.entryIs, k.subdir, k.entryIs),
	}
	cmd.AddCommand(
		newArtifactInstallCmd(k),
		newArtifactUninstallCmd(k),
		newArtifactListCmd(k),
	)
	return cmd
}

// scopeFlag declares the --scope flag every artifact verb takes, returning the
// pointer its RunE reads. Written once because install / uninstall / list must
// agree on the vocabulary AND the default: three copies would let one drift
// (a reworded help string, a changed default) with nothing to catch it.
func scopeFlag(cmd *cobra.Command) *string {
	var scope string
	cmd.Flags().StringVar(&scope, "scope", "project", "install scope: project (= cwd/.claude) | user (= ~/.claude)")
	return &scope
}

func newArtifactInstallCmd(k artifactKind) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: fmt.Sprintf("Install bough's canonical %ss into .claude/%s", k.entryIs, k.subdir),
	}
	scope := scopeFlag(cmd)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		dst, err := artifactDir(k, HookScope(*scope))
		if err != nil {
			return err
		}
		names, err := embeddedArtifactNames(k)
		if err != nil {
			return err
		}
		// Which entries are already there is read BEFORE deploying, because
		// after the write every name looks identical. bough ships names as
		// generic as list.md and status.md, so a collision is as likely to be
		// the operator's own file as bough's older copy — and bough cannot tell
		// the two apart. It overwrites either way (that is how an upgrade picks
		// up new content), so the least it can do is say which entries it
		// replaced rather than reporting a uniform "installed".
		replaced := make(map[string]bool, len(names))
		for _, n := range names {
			if _, err := os.Lstat(filepath.Join(dst, n)); err == nil {
				replaced[n] = true
			}
		}
		if err := procutil.DeployAssets(bough.Assets, k.subdir, dst); err != nil {
			return fmt.Errorf("install %ss into %s: %w", k.entryIs, dst, err)
		}
		fmt.Fprintf(c.OutOrStdout(), "installed %d %s(s) into %s\n", len(names), k.entryIs, dst)
		for _, n := range names {
			mark := "new"
			if replaced[n] {
				mark = "replaced"
			}
			fmt.Fprintf(c.OutOrStdout(), "  %-24s %s\n", n, mark)
		}
		return nil
	}
	return cmd
}

func newArtifactUninstallCmd(k artifactKind) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: fmt.Sprintf("Remove bough's %ss from .claude/%s (leaves your own entries alone)", k.entryIs, k.subdir),
	}
	scope := scopeFlag(cmd)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		dst, err := artifactDir(k, HookScope(*scope))
		if err != nil {
			return err
		}
		// Only names bough ships are removed. Anything else in the
		// directory is the operator's and stays put — the same contract
		// `bough claude hook uninstall` keeps for hand-edited settings.
		names, err := embeddedArtifactNames(k)
		if err != nil {
			return err
		}
		removed := 0
		for _, n := range names {
			p := filepath.Join(dst, n)
			if _, err := os.Lstat(p); err != nil {
				continue // not installed at this scope
			}
			if err := os.RemoveAll(p); err != nil {
				fmt.Fprintf(c.ErrOrStderr(), "  remove %s: %v\n", n, err)
				continue
			}
			removed++
		}
		fmt.Fprintf(c.OutOrStdout(), "removed %d bough %s(s) from %s\n", removed, k.entryIs, dst)
		return nil
	}
	return cmd
}

func newArtifactListCmd(k artifactKind) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: fmt.Sprintf("Show which bough %ss are installed at the given scope", k.entryIs),
	}
	scope := scopeFlag(cmd)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		dst, err := artifactDir(k, HookScope(*scope))
		if err != nil {
			return err
		}
		names, err := embeddedArtifactNames(k)
		if err != nil {
			return err
		}
		return renderArtifactList(c.OutOrStdout(), k, dst, names)
	}
	return cmd
}

// renderArtifactList prints one line per artifact bough ships, marking whether
// it is present at this scope. Split out so the formatting is unit-testable
// without constructing a cobra command or touching a real .claude dir.
func renderArtifactList(w io.Writer, k artifactKind, dst string, names []string) error {
	fmt.Fprintf(w, "bough %ss (%s)\n", k.entryIs, dst)
	for _, n := range names {
		mark := "not installed"
		if _, err := os.Lstat(filepath.Join(dst, n)); err == nil {
			mark = "✓ installed"
		}
		fmt.Fprintf(w, "  %-24s %s\n", n, mark)
	}
	return nil
}

// artifactDir resolves .claude/<subdir> for the scope, reusing the same scope
// vocabulary (project | user) `bough claude hook` uses so the two never drift.
//
// Project scope lands where linkWorktreeArtifacts (create.go) points every
// worktree's .claude/<subdir> symlink, so installing once at the monorepo root
// reaches the worktree sessions too — provided cwd is the identity root those
// symlinks resolve against (resolveIdentityRoot, see #60).
func artifactDir(k artifactKind, scope HookScope) (string, error) {
	dir, err := claudeDir(scope)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, k.subdir), nil
}

// embeddedArtifactNames lists what bough ships for this kind, straight from the
// embedded FS — so install / uninstall / list agree on the set by construction
// and a newly added skill needs no second registration anywhere.
func embeddedArtifactNames(k artifactKind) ([]string, error) {
	entries, err := bough.Assets.ReadDir(k.subdir)
	if err != nil {
		return nil, fmt.Errorf("read embedded %s: %w", k.subdir, err)
	}
	var names []string
	for _, e := range entries {
		if k.dirEntry != e.IsDir() {
			continue // skills are dirs, commands are .md files
		}
		if !k.dirEntry && !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}
