//go:build linux

package export

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// Linux's whence values for seeking data and holes, which the syscall
// package does not name.
const (
	seekData = 3
	seekHole = 4
)

// copyKeepingHoles copies size bytes of source into destination and keeps
// the holes: it seeks each run of data in the source, writes it at its own
// offset, and sets the destination's length last, so that a trailing hole
// stays one. A raw disk image is mostly holes, and a copy that wrote every
// zero would fill the drive dist/ is on. A file system that cannot seek
// data is copied whole. report is told how far the copy has got, holes
// included.
func copyKeepingHoles(source, destination *os.File, size int64, report func(copied int64)) error {
	offset := int64(0)
	for offset < size {
		dataStart, err := source.Seek(offset, seekData)
		if errors.Is(err, syscall.ENXIO) {
			break // no data from here on: the rest is a hole
		}
		if err != nil {
			if offset == 0 {
				return copyWhole(source, destination, size, report)
			}
			return err
		}
		dataEnd, err := source.Seek(dataStart, seekHole)
		if err != nil {
			return err
		}
		if _, err := source.Seek(dataStart, io.SeekStart); err != nil {
			return err
		}
		if _, err := destination.Seek(dataStart, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.CopyN(destination, source, dataEnd-dataStart); err != nil {
			return err
		}
		report(dataEnd)
		offset = dataEnd
	}
	if err := destination.Truncate(size); err != nil {
		return err
	}
	report(size)
	return nil
}
