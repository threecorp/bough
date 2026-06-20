//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd

package observer

import "os"

// statSys is a no-op on platforms where syscall.Stat_t.Ino is not
// available. The file watch observer reports rotation handling as
// "best-effort, off" on these platforms.
func statSys(_ os.FileInfo) (uint64, bool) { return 0, false }
