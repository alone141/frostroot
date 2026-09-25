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

// Stage moves sourcePath next to destinationPath under a temporary name and
// returns that name, so that the caller can put the file in place with one
// rename once whatever else has to land first has landed. The builder
// renames the lock into place between the two: a lock that cannot be renamed
// then fails while the previous tarball is still whole. Stage renames when
// both paths are on one filesystem. Across filesystems (EXDEV) it copies
// into the temporary file and syncs it, so the destination directory never
// holds a half-written file under the final name. Under WSL the copy is the
// normal case: the work directory is on the Linux disk and dist/ often sits
// on a Windows drive. onCopyProgress may be nil; a rename reports nothing.
func Stage(sourcePath, destinationPath string, onCopyProgress CopyProgressFunc) (stagedPath string, err error) {
	return stage(sourcePath, destinationPath, os.Rename, onCopyProgress)
}

// Unstage undoes Stage for a file that is not going into place after all:
// one that Stage moved goes back to sourcePath, and one it copied is
// removed, so that the source is where it was and the destination directory
// holds nothing of it. Best effort: the caller is failing already, and that
// error is the one it returns.
func Unstage(stagedPath, sourcePath string) {
	if _, err := os.Lstat(sourcePath); err == nil {
		// Still there, so Stage copied it.
		_ = os.Remove(stagedPath)
		return
	}
	if err := os.Rename(stagedPath, sourcePath); err != nil {
		_ = os.Remove(stagedPath)
	}
}

// renameFunc has the signature of os.Rename. Tests pass their own to simulate
// a move across filesystems.
type renameFunc func(oldPath, newPath string) error

func stage(sourcePath, destinationPath string, rename renameFunc, onCopyProgress CopyProgressFunc) (string, error) {
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return "", err
	}
	// The name is claimed first, so that no other build takes it, and the
	// source is renamed over the claim: one directory, so anything but EXDEV
	// is a real failure, and the source stays where it is.
	temporary, err := CreateTemp(filepath.Dir(destinationPath), "."+filepath.Base(destinationPath)+".*.tmp")
	if err != nil {
		return "", err
	}
	stagedPath := temporary.Name()
	err = rename(sourcePath, stagedPath)
	copied := false
	if errors.Is(err, syscall.EXDEV) {
		copied = true
		err = copyInto(sourcePath, temporary, onCopyProgress)
	}
	if closeErr := temporary.Close(); err == nil && copied {
		// A rename replaced the file this handle was on, so its close has
		// nothing to say; a copy's does.
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(stagedPath)
		return "", err
	}
	return stagedPath, nil
}

// copyInto copies the file at sourcePath into destination, reporting its
// progress, and syncs it, so that what the caller renames into place is on
// disk.
func copyInto(sourcePath string, destination *os.File, onCopyProgress CopyProgressFunc) error {
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
	var writer io.Writer = destination
	if onCopyProgress != nil {
		writer = &progressWriter{writer: destination, totalBytes: sourceInfo.Size(), onProgress: onCopyProgress}
	}
	if _, err := io.Copy(writer, source); err != nil {
		return err
	}
	return destination.Sync()
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
