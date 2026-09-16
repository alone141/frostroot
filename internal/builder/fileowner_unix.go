//go:build unix

package builder

import (
	"os"
	"syscall"
)

// fileOwner returns the uid that owns the file described by info.
func fileOwner(info os.FileInfo) (uid int, known bool) {
	systemStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(systemStat.Uid), true
}
