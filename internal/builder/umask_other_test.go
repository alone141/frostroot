//go:build !unix

package builder

import "testing"

// setUmask skips the test: there is no umask here, and so no file mode
// for it to check. It is defined so that GOOS=windows go vet compiles the
// package's tests.
func setUmask(t *testing.T, _ int) {
	t.Helper()
	t.Skip("this system has no umask")
}
