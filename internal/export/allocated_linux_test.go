//go:build linux

package export

import (
	"os"
	"syscall"
	"testing"
)

// allocated returns how many bytes path takes on disk, which holes do not.
func allocated(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("no Stat_t on Linux")
	}
	return stat.Blocks * 512
}
