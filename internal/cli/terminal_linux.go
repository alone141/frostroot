//go:build linux

package cli

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminal is the default terminal check: an *os.File whose driver
// answers a terminal's TCGETS request. A character device alone is not
// enough: /dev/null is one, and `frostroot init </dev/null`, meant to take
// every default, opened the full-screen form on an input already at its end.
// The request goes through SyscallConn, which leaves the file's mode alone,
// where Fd would make it blocking.
func isTerminal(stream any) bool {
	file, isFile := stream.(*os.File)
	if !isFile {
		return false
	}
	connection, err := file.SyscallConn()
	if err != nil {
		return false
	}
	answered := false
	err = connection.Control(func(descriptor uintptr) {
		var state syscall.Termios
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, descriptor, uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&state)))
		answered = errno == 0
	})
	return err == nil && answered
}
