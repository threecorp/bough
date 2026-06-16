//go:build darwin || linux

package dockerutil

import (
	"fmt"
	"net"
)

// IsPortFree probes whether the host's loopback port is currently free
// by attempting a short-lived `net.Listen`. A taken port short-circuits
// the per-plugin dockerUp before the daemon's generic "port is already
// allocated" error, so the operator sees an actionable message instead
// of having to grep `docker ps` for the conflict.
//
// CAVEAT: macOS Docker Desktop's vpnkit makes published ports look
// free to `net.Listen` because the listener lives in the VM, not on
// the host. Callers must therefore treat IsPortFree as a best-effort
// pre-flight and still detect the daemon's "port is already allocated"
// error on ContainerStart — see the per-plugin error rewrap.
func IsPortFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}
