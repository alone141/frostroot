//go:build !linux

package builder

import (
	"errors"
	"os"
)

// recipeDirLocking says that HoldRecipeDir holds nothing here: frostroot
// only builds on Linux (Build returns ErrNotLinux elsewhere), and this keeps
// the package compiling on other systems.
const recipeDirLocking = false

// lockExclusive is never called where recipeDirLocking is false.
func lockExclusive(string) (*os.File, bool, error) {
	return nil, false, errors.New("recipe directory holds are taken on Linux only")
}
