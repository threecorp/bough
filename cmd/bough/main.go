// Command bough is the per-worktree isolation orchestrator's host
// binary. The actual subcommand wiring lives in internal/cli so this
// file stays a thin entrypoint — easier to release, easier to unit-
// test the CLI against the underlying packages without paying for a
// fork()+exec() per test case.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ikeikeikeike/bough/internal/cli"
)

// version is overwritten by GoReleaser via -ldflags=-X.
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := cli.NewRootCmd(version)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "bough:", err)
		os.Exit(1)
	}
}
