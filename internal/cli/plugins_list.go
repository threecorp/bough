package cli

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ikeikeikeike/bough/internal/pluginhost"
	"github.com/spf13/cobra"
)

func newPluginsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugins",
		Short: "List DB plugins discoverable on PATH",
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

func runPluginsList(ctx context.Context, stdout interface{ Write([]byte) (int, error) }) error {
	// Brute-force scan of PATH for `bough-plugin-*` binaries. We don't
	// invoke them here — discovery alone is plenty for `bough plugins
	// list`; bringing each plugin up via gRPC would be heavyweight.
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
	kinds := make([]string, 0, len(seen))
	for k := range seen {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
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
	// indirection so unit tests can inject a fixture PATH via fakeExec
	out, err := exec.Command("sh", "-c", "printf %s \"$PATH\"").Output()
	if err != nil {
		return ""
	}
	return string(out)
}
