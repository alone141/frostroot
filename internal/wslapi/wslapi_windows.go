//go:build windows

package wslapi

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// launchPollInterval is how often Launch looks at its context while the
// command runs.
const launchPollInterval = 100 * time.Millisecond

// library is wslapi.dll, loaded on first use and kept: Windows counts the
// loads of one library, and unloading it under a call in flight would be
// worse than holding it until the process ends.
var library = sync.OnceValues(loadLibrary)

// loadLibrarySearchSystem32 is LoadLibraryExW's LOAD_LIBRARY_SEARCH_SYSTEM32:
// the libraries the loaded one needs come from the system directory alone.
const loadLibrarySearchSystem32 = 0x00000800

// loadLibrary loads wslapi.dll by its full path in the system directory,
// never by bare name, and the libraries it needs from the system directory
// too, never from the application's: import runs frostroot from a user's
// install directory, and a library planted beside it must not be what runs.
func loadLibrary() (*syscall.DLL, error) {
	systemDir, err := systemDirectory()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(systemDir, "wslapi.dll")
	kernel32, err := syscall.LoadDLL("kernel32.dll")
	if err != nil {
		return nil, fmt.Errorf("loading kernel32.dll: %w", err)
	}
	loadLibraryEx, err := kernel32.FindProc("LoadLibraryExW")
	if err != nil {
		return nil, fmt.Errorf("finding LoadLibraryExW: %w", err)
	}
	pathPointer, err := utf16("library path", path)
	if err != nil {
		return nil, err
	}
	handle, _, callErr := loadLibraryEx.Call(uintptr(unsafe.Pointer(pathPointer)), 0, loadLibrarySearchSystem32)
	if handle == 0 {
		return nil, fmt.Errorf("%w: loading %s: %w", ErrNoWSL, path, callErr)
	}
	return &syscall.DLL{Name: path, Handle: syscall.Handle(handle)}, nil
}

// systemDirectory returns Windows' system directory, C:\Windows\System32
// on most machines.
func systemDirectory() (string, error) {
	// kernel32.dll is one of Windows' known DLLs, which are loaded from the
	// system directory whatever the name says.
	kernel32, err := syscall.LoadDLL("kernel32.dll")
	if err != nil {
		return "", fmt.Errorf("loading kernel32.dll: %w", err)
	}
	getSystemDirectory, err := kernel32.FindProc("GetSystemDirectoryW")
	if err != nil {
		return "", fmt.Errorf("finding GetSystemDirectoryW: %w", err)
	}
	buffer := make([]uint16, syscall.MAX_PATH+1)
	length, _, callErr := getSystemDirectory.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if length == 0 || length >= uintptr(len(buffer)) {
		return "", fmt.Errorf("GetSystemDirectoryW: %w", callErr)
	}
	return syscall.UTF16ToString(buffer[:length]), nil
}

// function finds a function of wslapi.dll. A Windows without WSL has no
// wslapi.dll, and that is an error, not a panic.
func function(name string) (*syscall.Proc, error) {
	dll, err := library()
	if err != nil {
		return nil, err
	}
	found, err := dll.FindProc(name)
	if err != nil {
		return nil, fmt.Errorf("%w: wslapi.dll has no %s: %w", ErrNoWSL, name, err)
	}
	return found, nil
}

// utf16 converts an argument for a call; a string holding NUL has no
// UTF-16 form.
func utf16(what, value string) (*uint16, error) {
	pointer, err := syscall.UTF16PtrFromString(value)
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", what, value, err)
	}
	return pointer, nil
}

// IsRegistered reports whether a distribution named name is registered for
// this Windows user.
func IsRegistered(name string) (bool, error) {
	isRegistered, err := function(functionIsRegistered)
	if err != nil {
		return false, err
	}
	namePointer, err := utf16("distribution name", name)
	if err != nil {
		return false, err
	}
	result, _, _ := isRegistered.Call(uintptr(unsafe.Pointer(namePointer)))
	return uint32(result) != 0, nil
}

// Register registers the gzip-compressed tarball at tarballPath as the
// distribution name. Windows unpacks it into a rootfs directory beside the
// executable that calls this, whatever the working directory, and the call
// returns only once that is done.
func Register(name, tarballPath string) error {
	register, err := function(functionRegister)
	if err != nil {
		return err
	}
	namePointer, err := utf16("distribution name", name)
	if err != nil {
		return err
	}
	tarballPointer, err := utf16("tarball path", tarballPath)
	if err != nil {
		return err
	}
	result, _, _ := register.Call(uintptr(unsafe.Pointer(namePointer)), uintptr(unsafe.Pointer(tarballPointer)))
	return checkHRESULT(functionRegister, uint32(result))
}

// Unregister unregisters the distribution name and removes its file
// system. Windows leaves the directory it was in.
func Unregister(name string) error {
	unregister, err := function(functionUnregister)
	if err != nil {
		return err
	}
	namePointer, err := utf16("distribution name", name)
	if err != nil {
		return err
	}
	result, _, _ := unregister.Call(uintptr(unsafe.Pointer(namePointer)))
	return checkHRESULT(functionUnregister, uint32(result))
}

// Configure sets the user the distribution name starts as, by uid, and its
// flags.
func Configure(name string, defaultUID uint32, flags DistributionFlags) error {
	configure, err := function(functionConfigure)
	if err != nil {
		return err
	}
	namePointer, err := utf16("distribution name", name)
	if err != nil {
		return err
	}
	result, _, _ := configure.Call(uintptr(unsafe.Pointer(namePointer)), uintptr(defaultUID), uintptr(flags))
	return checkHRESULT(functionConfigure, uint32(result))
}

// launchRead is what Launch's reader got out of the pipe.
type launchRead struct {
	output []byte
	err    error
}

// Launch runs command in the distribution name, as its default user, and
// returns what it wrote to standard output and standard error together, at
// most MaxLaunchOutput bytes, and its exit code. It waits for the command to
// exit, or for ctx to end, whichever comes first; a command still running
// then is left to finish on its own.
func Launch(ctx context.Context, name, command string) (output []byte, exitCode uint32, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	launch, err := function(functionLaunch)
	if err != nil {
		return nil, 0, err
	}
	namePointer, err := utf16("distribution name", name)
	if err != nil {
		return nil, 0, err
	}
	commandPointer, err := utf16("command", command)
	if err != nil {
		return nil, 0, err
	}
	inheritable := syscall.SecurityAttributes{InheritHandle: 1}
	inheritable.Length = uint32(unsafe.Sizeof(inheritable))
	// Standard input is this process's own, as in the experiments that
	// found this call working; the commands Launch runs read none. A
	// process with none, started detached or by a service, gives the
	// command NUL instead.
	standardInput, err := syscall.GetStdHandle(syscall.STD_INPUT_HANDLE)
	if err != nil || standardInput == syscall.InvalidHandle || standardInput == 0 {
		nul, err := openNUL(&inheritable)
		if err != nil {
			return nil, 0, err
		}
		// The command has its own copy once WslLaunch returns.
		defer func() { _ = syscall.CloseHandle(nul) }() // read-only: closing it loses nothing
		standardInput = nul
	}
	var readEnd, writeEnd syscall.Handle
	if err := syscall.CreatePipe(&readEnd, &writeEnd, &inheritable, 0); err != nil {
		return nil, 0, fmt.Errorf("CreatePipe: %w", err)
	}
	// The command gets the write end; the read end stays this process's.
	if err := syscall.SetHandleInformation(readEnd, syscall.HANDLE_FLAG_INHERIT, 0); err != nil {
		_ = syscall.CloseHandle(readEnd)  // nothing was read or written
		_ = syscall.CloseHandle(writeEnd) // nothing was read or written
		return nil, 0, fmt.Errorf("SetHandleInformation: %w", err)
	}
	reader := os.NewFile(uintptr(readEnd), "WslLaunch output")
	var process syscall.Handle
	result, _, _ := launch.Call(
		uintptr(unsafe.Pointer(namePointer)), uintptr(unsafe.Pointer(commandPointer)),
		0, // FALSE: start in the default user's home, not this directory
		uintptr(standardInput), uintptr(writeEnd), uintptr(writeEnd),
		uintptr(unsafe.Pointer(&process)))
	// This process's copy of the write end must close, or the read never
	// ends.
	_ = syscall.CloseHandle(writeEnd) // a pipe end: closing it loses nothing
	if err := checkHRESULT(functionLaunch, uint32(result)); err != nil {
		_ = reader.Close() // read-only, and nothing will write
		return nil, 0, err
	}
	defer func() { _ = syscall.CloseHandle(process) }() // a process handle: closing it ends nothing

	reads := make(chan launchRead, 1)
	go func() {
		// The reader is closed here and only here: closing it while a read
		// is pending would wait for that read.
		defer func() { _ = reader.Close() }() // read-only
		kept, err := io.ReadAll(io.LimitReader(reader, MaxLaunchOutput+1))
		if err == nil && len(kept) > MaxLaunchOutput {
			// Drained, so that the command does not block on a full pipe.
			_, err = io.Copy(io.Discard, reader)
			if err == nil {
				err = ErrOutputTooLarge
			}
		}
		reads <- launchRead{output: kept, err: err}
	}()

	if err := waitForProcess(ctx, process); err != nil {
		return nil, 0, err
	}
	if err := syscall.GetExitCodeProcess(process, &exitCode); err != nil {
		return nil, 0, fmt.Errorf("GetExitCodeProcess: %w", err)
	}
	select {
	case read := <-reads:
		if read.err != nil {
			return nil, exitCode, fmt.Errorf("reading the output of %q: %w", command, read.err)
		}
		return read.output, exitCode, nil
	case <-ctx.Done():
		return nil, exitCode, ctx.Err()
	}
}

// openNUL opens the NUL device for reading, as a handle a started process
// inherits when attributes say so.
func openNUL(attributes *syscall.SecurityAttributes) (syscall.Handle, error) {
	name, err := utf16("device", "NUL")
	if err != nil {
		return 0, err
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, attributes, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("opening NUL for the command's standard input: %w", err)
	}
	return handle, nil
}

// waitForProcess waits for process to exit, looking at ctx every
// launchPollInterval.
func waitForProcess(ctx context.Context, process syscall.Handle) error {
	for {
		event, err := syscall.WaitForSingleObject(process, uint32(launchPollInterval/time.Millisecond))
		switch {
		case err != nil:
			return fmt.Errorf("WaitForSingleObject: %w", err)
		case event == syscall.WAIT_OBJECT_0:
			return nil
		case event != syscall.WAIT_TIMEOUT:
			return fmt.Errorf("WaitForSingleObject returned %#x", event)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
