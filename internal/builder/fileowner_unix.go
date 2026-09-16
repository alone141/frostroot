//go:build unix

package builder

import (
	"os"
	"syscall"
)

// fileOwner returns the uid that owns fi.
func fileOwner(fi os.FileInfo) (uid int, known bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
