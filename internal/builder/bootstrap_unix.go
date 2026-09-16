//go:build unix

package builder

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// interruptOnCancel runs cmd in its own process group and, on cancel, sends
// SIGINT to the whole group, which is what Ctrl-C in a terminal does.
// mmdebstrap depends on that: its main process answers SIGINT by waiting for
// its workers, and only the workers stop. Signalling the main process alone,
// as happens when frostroot is stopped by kill, timeout or a service manager
// rather than a terminal, would let the bootstrap run to the end.
//
// The separate group also keeps a terminal's Ctrl-C from reaching mmdebstrap
// twice, and frostroot out of the SIGHUP mmdebstrap sends to its own process
// group when writing the tarball fails. Nothing in the group reads the
// terminal: stdin is /dev/null and output goes through pipes.
func interruptOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
