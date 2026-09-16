// Package export names the finished image and moves it into place.
//
// It does not tar anything, and must not start to. mmdebstrap writes the
// tarball from inside its user namespace, which is the only place ownership,
// symlink targets, hardlinks and file capabilities come out right. Go's
// archive/tar walking a rootfs from outside gets all four wrong while every
// unit test still passes.
package export

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// TarballRelPath returns where the image lands, relative to the recipe
// directory.
func TarballRelPath(imageName, release, arch string) string {
	return filepath.Join("dist", fmt.Sprintf("%s-ubuntu-%s-%s.tar.gz", imageName, release, arch))
}

// CopyProgressFunc is told, after every chunk, how far a copy across
// filesystems has got. A rename reports nothing: it is instant.
type CopyProgressFunc func(copiedBytes, totalBytes int64)

// Place moves sourcePath to destinationPath, replacing any existing file. It
// renames when both are on one filesystem. Across filesystems (EXDEV) it copies
// to a temporary file next to the destination, syncs, renames over the
// destination and only then removes the source, so the destination is never
// seen half-written. Under WSL this is the normal case: the work directory is
// on the Linux disk and dist/ often sits on a Windows drive. onCopyProgress may
// be nil.
func Place(sourcePath, destinationPath string, onCopyProgress CopyProgressFunc) error {
	return place(sourcePath, destinationPath, os.Rename, onCopyProgress)
}

// renameFunc has the signature of os.Rename. Tests pass their own to simulate
// a move across filesystems.
type renameFunc func(oldPath, newPath string) error

func place(sourcePath, destinationPath string, rename renameFunc, onCopyProgress CopyProgressFunc) error {
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return err
	}
	err := rename(sourcePath, destinationPath)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyAcrossFilesystems(sourcePath, destinationPath, onCopyProgress); err != nil {
		return err
	}
	return os.Remove(sourcePath)
}

// copyAcrossFilesystems copies sourcePath over destinationPath through a
// temporary file in the destination directory.
func copyAcrossFilesystems(sourcePath, destinationPath string, onCopyProgress CopyProgressFunc) (err error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	// Closing a file opened only for reading cannot lose data, so its error
	// carries nothing worth returning.
	defer func() { _ = source.Close() }()
	sourceInfo, err := source.Stat()
	if err != nil {
		return err
	}

	temporary, err := CreateTemp(filepath.Dir(destinationPath), "."+filepath.Base(destinationPath)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			// Best effort: the copy has already failed, and that error is the
			// one returned.
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
		}
	}()
	var destination io.Writer = temporary
	if onCopyProgress != nil {
		destination = &progressWriter{writer: temporary, totalBytes: sourceInfo.Size(), onProgress: onCopyProgress}
	}
	if _, err = io.Copy(destination, source); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), destinationPath)
}

// progressWriter reports the running total of bytes written through it.
type progressWriter struct {
	writer      io.Writer
	writtenByte int64
	totalBytes  int64
	onProgress  CopyProgressFunc
}

func (w *progressWriter) Write(data []byte) (int, error) {
	written, err := w.writer.Write(data)
	w.writtenByte += int64(written)
	w.onProgress(w.writtenByte, w.totalBytes)
	return written, err
}

// maxTemporaryNameAttempts bounds CreateTemp's search for an unused name.
const maxTemporaryNameAttempts = 1000

// CreateTemp creates a new, uniquely named file in directory for output that
// will be renamed into place. pattern works as for os.CreateTemp: its last "*"
// is replaced by a random number. The file is created with mode 0644 (before
// the umask) instead of 0600, and never chmodded: chmod fails with EPERM on
// drvfs, the mount WSL uses for Windows drives, which is where a recipe
// directory and its dist/ often are.
func CreateTemp(directory, pattern string) (*os.File, error) {
	prefix, suffix := pattern, ""
	if starIndex := strings.LastIndex(pattern, "*"); starIndex >= 0 {
		prefix, suffix = pattern[:starIndex], pattern[starIndex+1:]
	}
	for range maxTemporaryNameAttempts {
		candidatePath := filepath.Join(directory, prefix+strconv.FormatUint(uint64(rand.Uint32()), 10)+suffix)
		file, err := os.OpenFile(candidatePath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return file, err
	}
	return nil, fmt.Errorf("could not create a temporary file in %s after %d attempts", directory, maxTemporaryNameAttempts)
}
