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

// TarballRelPath is where the image lands, relative to the recipe directory.
func TarballRelPath(imageName, release, arch string) string {
	return filepath.Join("dist", fmt.Sprintf("%s-ubuntu-%s-%s.tar.gz", imageName, release, arch))
}

// rename is replaced in tests to simulate a cross-device move.
var rename = os.Rename

// Place moves src to dest, replacing dest. It renames when src and dest share
// a filesystem. Across filesystems (EXDEV) it copies to a temporary file next
// to dest, syncs, renames over dest and only then removes src, so dest is
// never observed half-written. Under WSL this is the normal case: the work
// directory is on the Linux disk and dist/ often sits on a 9p mount of a
// Windows drive.
func Place(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	err := rename(src, dest)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyInto(src, dest); err != nil {
		return err
	}
	return os.Remove(src)
}

// CreateTemp creates a new, uniquely named file in dir for output that will
// be renamed into place. pattern is as for os.CreateTemp; the file is created
// with mode 0644 (before umask) instead of 0600, and never chmodded: chmod
// fails with EPERM on drvfs, the mount WSL uses for Windows drives, which is
// where a recipe directory and its dist/ often are.
func CreateTemp(dir, pattern string) (*os.File, error) {
	prefix, suffix, _ := strings.Cut(pattern, "*")
	for range 1000 {
		name := filepath.Join(dir, prefix+strconv.FormatUint(uint64(rand.Uint32()), 10)+suffix)
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, err
	}
	return nil, fmt.Errorf("could not create a temporary file in %s", dir)
}

func copyInto(src, dest string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = io.Copy(tmp, in); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}
