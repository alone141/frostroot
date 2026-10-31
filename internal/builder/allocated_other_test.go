//go:build !linux

package builder

import (
	"os"
	"testing"
)

// allocatedBytes reports a file as fully allocated: only Linux builds.
func allocatedBytes(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}
