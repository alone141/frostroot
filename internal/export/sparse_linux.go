//go:build linux

package export

import (
	"context"
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
// included. A canceled ctx stops it between two chunks.
func copyKeepingHoles(ctx context.Context, source, destination *os.File, size int64, report func(copied int64)) error {
	buffer := make([]byte, copyChunkBytes)
	offset := int64(0)
	for offset < size {
		dataStart, err := source.Seek(offset, seekData)
		if errors.Is(err, syscall.ENXIO) {
			break // no data from here on: the rest is a hole
		}
		if err != nil {
			if offset == 0 {
				return copyWhole(ctx, source, destination, size, report)
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
		// Through the progress writer, chunk by chunk: a tarball is one run
		// of data, and a report at its end alone left the bar at 0 for the
		// whole copy onto a Windows drive.
		run := &progressWriter{ctx: ctx, writer: destination, writtenByte: dataStart, report: report}
		if err := copyChunks(run, io.LimitReader(source, dataEnd-dataStart), dataEnd-dataStart, buffer); err != nil {
			return err
		}
		offset = dataEnd
	}
	if err := destination.Truncate(size); err != nil {
		return err
	}
	report(size)
	return nil
}
