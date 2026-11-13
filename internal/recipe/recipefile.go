package recipe

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MaxKeyFileBytes bounds a source's key file. A vendor's keyring is a few
// kilobytes, and a mebibyte is what frostroot fetches a key with, so every
// key it saved fits.
const MaxKeyFileBytes = 1 << 20

// MaxCertificateFileBytes bounds a certificate file. A bundle of a few
// hundred authorities is a few hundred kibibytes, more with the text some
// distributions print above each one; four mebibytes leaves room for that
// and still stops a link to /dev/zero.
const MaxCertificateFileBytes = 4 << 20

// ReadKeyFile reads the key file keyPath names in recipeDir, as
// readInsideRecipeDirectory does.
func ReadKeyFile(recipeDir, keyPath string) ([]byte, error) {
	return readInsideRecipeDirectory(recipeDir, keyPath, MaxKeyFileBytes)
}

// ReadCertificateFile reads the certificate file certificatePath names in
// recipeDir, as readInsideRecipeDirectory does.
func ReadCertificateFile(recipeDir, certificatePath string) ([]byte, error) {
	return readInsideRecipeDirectory(recipeDir, certificatePath, MaxCertificateFileBytes)
}

// readInsideRecipeDirectory reads the file relativePath names in recipeDir,
// which is every way a key or certificate file a recipe names is read. The
// textual check alone lets a symbolic link lead anywhere: a cloned recipe's
// keys/vendor.asc could be a host's file, which the lock would then pin, or
// /dev/zero or a named pipe, which a read never finishes. So the links are
// resolved, the recipe directory's own too, and the file must be inside the
// directory, be a regular file before it is opened, and be at most maxBytes
// long. A link that stays inside the directory is fine. The error does not
// name the file; the caller says which one, as the recipe wrote it.
func readInsideRecipeDirectory(recipeDir, relativePath string, maxBytes int64) ([]byte, error) {
	if !insideRecipeDirectory(relativePath) {
		return nil, errors.New("not a relative path inside the recipe directory")
	}
	resolvedDir, err := resolvePath(recipeDir)
	if err != nil {
		return nil, fmt.Errorf("resolving the recipe directory: %w", err)
	}
	resolvedFile, err := resolvePath(filepath.Join(recipeDir, filepath.FromSlash(relativePath)))
	if err != nil {
		return nil, explainUnresolvable(recipeDir, relativePath, err)
	}
	if err := checkInside(resolvedDir, resolvedFile); err != nil {
		return nil, err
	}
	info, err := os.Stat(resolvedFile)
	if err != nil {
		return nil, withoutPath(err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("it is %s, not a regular file", describeFileKind(info.Mode()))
	}
	file, err := openCheckedFile(resolvedFile, info)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }() // read only
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, withoutPath(err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("it is larger than %d KiB, the most such a file may be", maxBytes>>10)
	}
	return data, nil
}

// PrepareFilePath creates the directories the file relativePath names in
// recipeDir goes in, as os.MkdirAll would, and returns the path to write
// the file at, with every link on the way resolved. It is the write side of
// readInsideRecipeDirectory: a cloned recipe's keys/ or certs/ can be a
// symbolic link to anywhere, and a key or certificate saved through it
// would land there. So the deepest directory that already exists is
// resolved and must be inside the resolved recipe directory before
// anything is created, the rest is created under the resolved path, and
// the result is resolved and checked again. A link that stays inside the
// directory is fine. The error does not name the file; the caller says
// which one, as the recipe wrote it.
//
// The caller writes at the returned path, which held no link when it was
// checked. Only someone who can change the recipe directory between the
// check and the write can still lead the file elsewhere, and they can
// change the recipe as well.
func PrepareFilePath(recipeDir, relativePath string) (string, error) {
	if !insideRecipeDirectory(relativePath) {
		return "", errors.New("not a relative path inside the recipe directory")
	}
	resolvedRecipeDir, err := resolvePath(recipeDir)
	if err != nil {
		return "", fmt.Errorf("resolving the recipe directory: %w", err)
	}
	components := strings.Split(path.Dir(path.Clean(filepath.ToSlash(relativePath))), "/")
	if components[0] == "." {
		components = nil
	}
	existing := recipeDir
	created := 0
	for _, component := range components {
		if _, err := os.Lstat(filepath.Join(existing, component)); err != nil {
			break
		}
		existing = filepath.Join(existing, component)
		created++
	}
	resolvedExisting, err := resolvePath(existing)
	if err != nil {
		return "", explainUnresolvable(recipeDir, relativePath, err)
	}
	if err := checkInside(resolvedRecipeDir, resolvedExisting); err != nil {
		return "", err
	}
	directory := filepath.Join(append([]string{resolvedExisting}, components[created:]...)...)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", withoutPath(err)
	}
	resolvedDirectory, err := resolvePath(directory)
	if err != nil {
		return "", withoutPath(err)
	}
	if err := checkInside(resolvedRecipeDir, resolvedDirectory); err != nil {
		return "", err
	}
	return filepath.Join(resolvedDirectory, path.Base(filepath.ToSlash(relativePath))), nil
}

// checkInside reports, as the refusals of a file a recipe names say it,
// that resolvedPath is not inside resolvedRecipeDir, or nil.
func checkInside(resolvedRecipeDir, resolvedPath string) error {
	relative, err := filepath.Rel(resolvedRecipeDir, resolvedPath)
	if err != nil || !filepath.IsLocal(relative) {
		return fmt.Errorf("a symbolic link leads it outside the recipe directory, to %s", resolvedPath)
	}
	return nil
}

// openCheckedFile opens path, which was stat'ed as checked. What was opened
// must be what was checked: a file swapped for another in between is
// refused rather than read.
func openCheckedFile(path string, checked fs.FileInfo) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, withoutPath(err)
	}
	opened, err := file.Stat()
	if err == nil && !os.SameFile(checked, opened) {
		err = errors.New("it changed while it was read")
	}
	if err != nil {
		_ = file.Close() // read only
		return nil, withoutPath(err)
	}
	return file, nil
}

// explainUnresolvable says why the links in relativePath under recipeDir
// could not be resolved, which err, from filepath.EvalSymlinks, says only
// as "no such file or directory" or "EvalSymlinks: too many links". The
// first link on the way that leads nowhere, or loops, is named with where
// it points.
func explainUnresolvable(recipeDir, relativePath string, err error) error {
	components := strings.Split(relativePath, "/")
	current := recipeDir
	for index, component := range components {
		current = filepath.Join(current, component)
		info, lstatErr := os.Lstat(current)
		if lstatErr != nil {
			break
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		_, statErr := os.Stat(current)
		if statErr == nil {
			continue
		}
		subject := "it"
		if index < len(components)-1 {
			subject = strings.Join(components[:index+1], "/")
		}
		target, readErr := os.Readlink(current)
		if readErr != nil {
			target = "an unreadable target"
		}
		if errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("%s is a symbolic link to %s, which does not exist", subject, target)
		}
		return fmt.Errorf("%s is a symbolic link to %s, which cannot be followed: the links loop, or are too many", subject, target)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		// Only the limit on links followed is not a PathError.
		return errors.New("it goes through too many symbolic links, or links that loop")
	}
	return pathErr.Err
}

// resolvePath returns path made absolute with every symbolic link in it
// resolved.
func resolvePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

// withoutPath drops the absolute path an os error carries, which the caller
// replaces with the path the recipe wrote. errors.Is still sees the cause.
func withoutPath(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

// describeFileKind names what a file that is not a regular one is.
func describeFileKind(mode fs.FileMode) string {
	switch {
	case mode.IsDir():
		return "a directory"
	case mode&fs.ModeNamedPipe != 0:
		return "a named pipe"
	case mode&fs.ModeSocket != 0:
		return "a socket"
	case mode&fs.ModeDevice != 0:
		return "a device"
	default:
		return "a special file"
	}
}
