package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ikeikeikeike/bough/internal/pluginhost"
	"github.com/spf13/cobra"
)

func newPluginsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "plugins",
		// PR #23 (Ν-1.8) added a `bough plugins verify` subcommand
		// and updated this Short text to "List and verify ...", but
		// the v0.9.0 reset (eee8a3d) deleted internal/cli/plugin_verify.go
		// and the AddCommand(..., newPluginsVerifyCmd()) call without
		// reverting this string — leaving `bough plugins --help` and
		// `bough plugins -h` advertising a "verify" capability that
		// does not exist (docs/SIGNING.md already documents its
		// absence accurately: "There is no `bough plugins verify`
		// subcommand today"). Restored to match the single `list`
		// subcommand actually wired below.
		Short: "List bough plugin binaries discoverable on PATH",
	}
	cmd.AddCommand(newPluginsListCmd())
	return cmd
}

func newPluginsListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print every bough-plugin-<kind> binary visible on PATH",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPluginsList(cmd.Context(), cmd.OutOrStdout())
		},
	}
	return cmd
}

// discoverPluginBinaries scans PATH for `bough-plugin-*` and returns
// kind → binary path, first match per kind winning as PATH order does.
// It resolves the same way pluginhost.Discover does, so what it reports
// is what an engine would actually launch.
func discoverPluginBinaries() map[string]string {
	dirs := strings.Split(pathEnv(), string(filepath.ListSeparator))
	seen := map[string]string{}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "bough-plugin-*"))
		for _, m := range matches {
			kind := strings.TrimPrefix(filepath.Base(m), "bough-plugin-")
			if _, ok := seen[kind]; !ok {
				seen[kind] = m
			}
		}
	}
	return seen
}

func sortedKinds(seen map[string]string) []string {
	kinds := make([]string, 0, len(seen))
	for k := range seen {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

func runPluginsList(ctx context.Context, stdout interface{ Write([]byte) (int, error) }) error {
	// Discovery only: `bough plugins list` answers "what is installed",
	// not "does it run". `bough doctor` answers the second one, because
	// launching every plugin is too heavyweight for a listing.
	seen := discoverPluginBinaries()
	kinds := sortedKinds(seen)
	if len(kinds) == 0 {
		fmt.Fprintln(stdout, "(no bough-plugin-* binaries on PATH — install bough-plugin-mysql etc.)")
		return nil
	}
	for _, k := range kinds {
		fmt.Fprintf(stdout, "%s\t%s\n", k, seen[k])
	}
	// Touch pluginhost / ctx symbols so future "verify discoverable"
	// logic can call Discover(kind) without re-importing.
	_, _ = ctx, pluginhost.Discover
	return nil
}

func pathEnv() string {
	return os.Getenv("PATH")
}
