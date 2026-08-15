package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/ikeikeikeike/bough/internal/termio"
)

// renderEnginePlugins reports whether the installed engine plugins can
// actually be launched — not whether their files exist.
//
// The distinction is the whole point. A plugin whose code signature no
// longer matches its bytes — a partial write, a patched file, a bad
// install — is still present, still executable, still the right size,
// and macOS kills it on exec (SIGKILL, exit 137). Nothing bough did
// before this said so: `bough --version` and every `--help` run fine,
// because none of them launches a plugin. The first symptom was a
// worktree create failing at engine startup, several steps from the
// cause.
//
// So doctor spends one short exec per plugin. A go-plugin binary run
// without its handshake refuses and exits non-zero — that IS a healthy
// plugin, and the only failures reported here are the ones that mean the
// binary cannot run at all: killed by a signal, or refused by the OS.
func renderEnginePlugins(ctx context.Context, w io.Writer) {
	seen := discoverPluginBinaries()
	if len(seen) == 0 {
		return // nothing installed here; a diagnostic with no subject says nothing
	}

	var broken []string
	for _, kind := range sortedKinds(seen) {
		if reason := probePluginBinary(ctx, seen[kind]); reason != "" {
			broken = append(broken, fmt.Sprintf("%s (%s)", kind, reason))
		}
	}

	fmt.Fprintln(w)
	st := termio.NewStyler(w)
	status := termio.StatusOK
	if len(broken) > 0 {
		status = termio.StatusError
	}
	// Population rather than a bare verdict, matching the worktree
	// isolation block: "0 of 0 start" and "5 of 5 start" are different
	// facts and a boolean cannot tell them apart.
	fmt.Fprintf(w, "%s Engine plugins:\n", st.Section(status))
	fmt.Fprintf(w, "    %s installed         %d of %d start\n",
		st.Mark(status), len(seen)-len(broken), len(seen))
	if len(broken) == 0 {
		return
	}
	fmt.Fprintf(w, "    %s cannot start      %s\n", st.Mark(termio.StatusError), namedList(broken))
	fmt.Fprintf(w, "        the file is there and the OS will not run it — reinstall it (delete\n")
	fmt.Fprintf(w, "        first, then copy), or on macOS re-sign: codesign --force --sign - <path>\n")
}

// probePluginBinary runs the binary and returns "" when it started, or a
// short reason when it could not. Seeded here rather than package scope
// so the deadline stays local to the one caller that needs it.
func probePluginBinary(ctx context.Context, path string) string {
	const probeTimeout = 5 * time.Second

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	// CombinedOutput is discarded: a plugin's refusal message is expected
	// noise, and doctor reports the outcome, not the plugin's opinion.
	cmd := exec.CommandContext(ctx, path)
	err := cmd.Run()
	if err == nil {
		return ""
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// Exited on its own — including the non-zero refusal a go-plugin
		// binary gives when launched without a handshake. Only death by
		// signal means the binary never got to run.
		if status, ok := exitErr.Sys().(interface{ Signaled() bool }); ok && status.Signaled() {
			return fmt.Sprintf("killed, %s", exitErr.String())
		}
		return ""
	}
	if ctx.Err() != nil {
		return fmt.Sprintf("no exit within %s", probeTimeout)
	}
	return err.Error()
}
