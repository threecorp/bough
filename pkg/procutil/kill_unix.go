//go:build darwin || linux

package procutil

import (
	"bufio"
	"bytes"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// KillStrayProcessCompose SIGTERM-s any process-compose subprocess whose
// cwd lives under cwdPrefix. Without this step the supervisor would
// respawn the engine (mysqld, redis-server, ...) immediately after a
// SIGKILL.
//
// macOS lsof requires -a (AND semantics) when combining -p with other
// filters; without it lsof joins the filters with OR and the caller
// risks killing unrelated supervisors. We only need cwd here so a bare
// lsof -p PID is sufficient.
func KillStrayProcessCompose(cwdPrefix string) {
	out, err := exec.Command("pgrep", "-f", "process-compose").Output()
	if err != nil {
		return
	}
	for _, ps := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(ps)
		if err != nil {
			continue
		}
		cwdOut, err := exec.Command("lsof", "-p", ps).Output()
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(cwdOut))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 9 && fields[3] == "cwd" {
				// Exact match or a real path-separator boundary — a bare
				// HasPrefix would also match ".../auba-api-1394" against
				// cwdPrefix ".../auba-api-139", SIGTERMing a sibling
				// worktree's still-running supervisor.
				if cwd := fields[len(fields)-1]; cwd == cwdPrefix || strings.HasPrefix(cwd, cwdPrefix+"/") {
					_ = syscall.Kill(pid, syscall.SIGTERM)
				}
				break
			}
		}
	}
}
