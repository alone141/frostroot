//go:build !linux

package export

import (
	"os"
	"testing"
)

// allocated reports a file as fully allocated: only Linux keeps holes here.
func allocated(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}
