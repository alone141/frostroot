//go:build !unix

package builder

import (
	"os"
	"os/exec"
)

// interruptOnCancel sends os.Interrupt to command on cancel. frostroot only
// builds on Linux (Build returns ErrNotLinux elsewhere); this keeps the package
// compiling on other systems.
func interruptOnCancel(command *exec.Cmd) {
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
}
