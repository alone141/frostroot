//go:build !windows

package wslapi

import "context"

// IsRegistered returns ErrNotWindows: there is no WSL API here.
func IsRegistered(string) (bool, error) { return false, ErrNotWindows }

// Register returns ErrNotWindows: there is no WSL API here.
func Register(string, string) error { return ErrNotWindows }

// Unregister returns ErrNotWindows: there is no WSL API here.
func Unregister(string) error { return ErrNotWindows }

// Configure returns ErrNotWindows: there is no WSL API here.
func Configure(string, uint32, DistributionFlags) error { return ErrNotWindows }

// Launch returns ErrNotWindows: there is no WSL API here.
func Launch(context.Context, string, string) (output []byte, exitCode uint32, err error) {
	return nil, 0, ErrNotWindows
}
