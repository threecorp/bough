package procutil

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// LsofListener returns the PID of whichever process holds the TCP
// listener on port, or 0 when nothing is listening. lsof's -t flag
// prints PIDs only, one per line; we take the first.
func LsofListener(port int) int {
	out, err := exec.Command("lsof", fmt.Sprintf("-tiTCP:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0
	}
	if i := strings.IndexAny(s, "\n\t "); i >= 0 {
		s = s[:i]
	}
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return pid
}
