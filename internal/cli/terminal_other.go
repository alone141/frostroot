//go:build !linux

package cli

// isTerminal is the default terminal check off Linux, where frostroot only
// explains that it needs Linux: a character device, as a terminal is.
func isTerminal(stream any) bool { return isCharacterDevice(stream) }
