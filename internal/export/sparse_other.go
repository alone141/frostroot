//go:build !linux

package export

import "os"

// copyKeepingHoles copies the whole file: only Linux seeks holes, and only
// Linux builds.
func copyKeepingHoles(source, destination *os.File, size int64, report func(copied int64)) error {
	return copyWhole(source, destination, size, report)
}
