//go:build !unix

package builder

import (
	"os"
	"os/exec"
)

// interruptOnCancel: frostroot only builds on Linux (Build returns
// ErrNotLinux elsewhere); this keeps the package compiling on other systems.
func interruptOnCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
}
