//go:build darwin || linux || freebsd || openbsd || netbsd

package observer

import (
	"os"
	"syscall"
)

// statSys returns the inode number for the given FileInfo on
// POSIX-like platforms. Used by the file watch observer to detect
// log-rotation events (the path's inode changes when an editor
// rewrites a file save-and-replace).
func statSys(fi os.FileInfo) (uint64, bool) {
	if s, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(s.Ino), true
	}
	return 0, false
}
