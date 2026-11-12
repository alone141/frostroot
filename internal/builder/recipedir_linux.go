//go:build linux

package builder

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// recipeDirLocking says that HoldRecipeDir holds anything here.
const recipeDirLocking = true

// lockExclusive opens the file at path, creating it, and takes an exclusive
// flock on it without waiting. held is false, with no error, when another
// open file holds it already.
func lockExclusive(path string) (file *os.File, held bool, err error) {
	file, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, err
	}
	connection, err := file.SyscallConn()
	if err != nil {
		_ = file.Close() // failing already; that error is the one returned
		return nil, false, err
	}
	var lockErr error
	if err := connection.Control(func(descriptor uintptr) {
		lockErr = syscall.Flock(int(descriptor), syscall.LOCK_EX|syscall.LOCK_NB)
	}); err != nil {
		lockErr = err
	}
	if lockErr != nil {
		_ = file.Close() // not ours to write; nothing written
		if errors.Is(lockErr, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, lockErr
	}
	return file, true, nil
}

// directoryIdentity returns the device and inode numbers of the directory
// at path, which no two directories share at one time, and false when it
// cannot be stat'ed.
func directoryIdentity(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("device %d inode %d", status.Dev, status.Ino), true
}
