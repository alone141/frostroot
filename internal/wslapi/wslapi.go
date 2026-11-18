// Package wslapi calls the functions of Windows' wslapi.dll that register,
// configure, run in and unregister a WSL distribution: the calls the Store's
// launchers make, which work on a Windows whose wsl.exe has no --import.
//
// It is the only place wslapi.dll is named. It imports no other frostroot
// package and decides nothing: the caller says what to register and how.
// On anything but Windows every function returns ErrNotWindows.
package wslapi

import (
	"errors"
	"fmt"
)

// DistributionFlags are the settings WslConfigureDistribution takes, as
// WSL_DISTRIBUTION_FLAGS defines them.
type DistributionFlags uint32

// The flags of a distribution.
const (
	// FlagEnableInterop lets the distribution start Windows programs.
	FlagEnableInterop DistributionFlags = 0x1
	// FlagAppendNTPath appends Windows' PATH to the distribution's.
	FlagAppendNTPath DistributionFlags = 0x2
	// FlagEnableDriveMounting mounts the Windows drives under /mnt.
	FlagEnableDriveMounting DistributionFlags = 0x4
	// DefaultFlags are the three, which wsl --import and the Store's
	// launchers give a distribution.
	DefaultFlags = FlagEnableInterop | FlagAppendNTPath | FlagEnableDriveMounting
)

// Errors a caller can tell apart with errors.Is.
var (
	// ErrNotWindows is every function's answer off Windows.
	ErrNotWindows = errors.New("the WSL API is only on Windows")
	// ErrNoWSL means this Windows has no wslapi.dll, a wslapi.dll without
	// the function asked for, or WSL not enabled.
	ErrNoWSL = errors.New("this Windows has no Windows Subsystem for Linux, or it is not enabled")
	// ErrTarballRefused is WslRegisterDistribution's E_FAIL: Windows
	// could not unpack the tarball.
	ErrTarballRefused = errors.New("the tarball was refused: Windows could not unpack it")
	// ErrAlreadyRegistered means a distribution of that name exists.
	ErrAlreadyRegistered = errors.New("a distribution of that name is already registered")
	// ErrAccessDenied is E_ACCESSDENIED.
	ErrAccessDenied = errors.New("access denied")
	// ErrOutputTooLarge means a command Launch ran wrote more than
	// MaxLaunchOutput bytes.
	ErrOutputTooLarge = fmt.Errorf("the command wrote more than %d bytes", MaxLaunchOutput)
)

// MaxLaunchOutput bounds what Launch reads of a command's output, which
// comes out of a distribution and is hostile input.
const MaxLaunchOutput = 64 << 10

// HRESULTs the functions are known to return. A failed HRESULT has its top
// bit set; one of the form 0x8007XXXX carries the Win32 error XXXX.
const (
	hresultFail            = 0x80004005 // E_FAIL
	hresultAccessDenied    = 0x80070005 // E_ACCESSDENIED, Win32 ERROR_ACCESS_DENIED
	hresultAlreadyExists   = 0x800700B7 // Win32 ERROR_ALREADY_EXISTS
	hresultLinuxNotPresent = 0x8007019E // Win32 ERROR_LINUX_SUBSYSTEM_NOT_PRESENT
)

// The functions of wslapi.dll this package calls, by their exported names.
const (
	functionIsRegistered = "WslIsDistributionRegistered"
	functionRegister     = "WslRegisterDistribution"
	functionUnregister   = "WslUnregisterDistribution"
	functionConfigure    = "WslConfigureDistribution"
	functionLaunch       = "WslLaunch"
)

// HRESULTError is a function of wslapi.dll that returned a failed HRESULT.
// It unwraps to one of this package's errors when the code is one it knows.
type HRESULTError struct {
	Function string // the function's name, such as WslRegisterDistribution
	Code     uint32
}

func (e *HRESULTError) Error() string {
	if known := e.Unwrap(); known != nil {
		return fmt.Sprintf("%s: %v (0x%08X)", e.Function, known, e.Code)
	}
	return fmt.Sprintf("%s failed with 0x%08X", e.Function, e.Code)
}

// Unwrap returns the error the code means, or nil for a code without one.
func (e *HRESULTError) Unwrap() error {
	switch e.Code {
	case hresultFail:
		// E_FAIL says nothing in general; from a registration it is what
		// the experiments saw for a tarball Windows could not unpack.
		if e.Function == functionRegister {
			return ErrTarballRefused
		}
	case hresultAccessDenied:
		return ErrAccessDenied
	case hresultAlreadyExists:
		return ErrAlreadyRegistered
	case hresultLinuxNotPresent:
		return ErrNoWSL
	}
	return nil
}

// checkHRESULT returns nil for a successful HRESULT and an HRESULTError for
// a failed one.
func checkHRESULT(function string, result uint32) error {
	if int32(result) >= 0 {
		return nil
	}
	return &HRESULTError{Function: function, Code: result}
}
