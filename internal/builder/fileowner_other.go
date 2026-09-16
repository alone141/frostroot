//go:build !unix

package builder

import "os"

// fileOwner reports the owner as unknown. frostroot only builds on Linux
// (Build returns ErrNotLinux elsewhere); this keeps the package compiling on
// other systems.
func fileOwner(os.FileInfo) (uid int, known bool) { return 0, false }
