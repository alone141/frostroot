//go:build unix

package export

import (
	"syscall"
	"testing"
)

// setUmask sets the process umask for the rest of the test.
func setUmask(t *testing.T, mask int) {
	t.Helper()
	previous := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(previous) })
}
