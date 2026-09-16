package builder

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"frostroot/internal/export"
)

// DefaultWorkRoot is where builds work when XDG_CACHE_HOME is unset. It is
// per user because /var/tmp is shared: a root-owned /var/tmp/frostroot left
// by one `sudo frostroot build` would block every later unprivileged build.
func DefaultWorkRoot(uid int) string {
	return "/var/tmp/frostroot-" + strconv.Itoa(uid)
}

// WorkRoot picks the directory under which each build gets its own work
// directory: $XDG_CACHE_HOME/frostroot, else DefaultWorkRoot. Never under
// /mnt: on WSL that is a 9p mount of a Windows drive, slow and unreliable for
// the chown and device-node work a bootstrap does.
func WorkRoot(getenv func(string) string, uid int) (string, error) {
	root := DefaultWorkRoot(uid)
	// The XDG base directory spec says relative paths are invalid; ignore them.
	if cache := getenv("XDG_CACHE_HOME"); filepath.IsAbs(cache) {
		root = filepath.Join(cache, "frostroot")
	}
	if strings.HasPrefix(root, "/mnt/") {
		return "", fmt.Errorf("%w: refusing to build under %s, a 9p mount of a Windows drive under WSL; set XDG_CACHE_HOME to a directory on the Linux filesystem", ErrBadWorkRoot, root)
	}
	return root, nil
}

// prepareWorkRoot creates root, 0755 whatever the umask so that mmdebstrap's
// user namespace can enter it, and checks that it is a directory owned by uid.
// A root owned by someone else is either a leftover from their build, which
// could not be written into anyway, or a squat in shared /var/tmp, which must
// not be used. A symbolic link the user owns is followed: pointing the work
// root at a bigger disk is legitimate.
func prepareWorkRoot(root string, uid int) error {
	if err := mkdirAllMode(root, 0o755); err != nil {
		return fmt.Errorf("%w: cannot create %s: %v; set XDG_CACHE_HOME to use another location", ErrBadWorkRoot, root, err)
	}
	fi, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadWorkRoot, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		if err := checkOwner(root, fi, uid); err != nil {
			return err
		}
		if fi, err = os.Stat(root); err != nil {
			return fmt.Errorf("%w: %v", ErrBadWorkRoot, err)
		}
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: %s is not a directory; remove it or set XDG_CACHE_HOME to use another location", ErrBadWorkRoot, root)
	}
	return checkOwner(root, fi, uid)
}

func checkOwner(path string, fi os.FileInfo, uid int) error {
	owner, known := fileOwner(fi)
	if known && owner != uid {
		return fmt.Errorf("%w: %s is owned by uid %d, not by you (uid %d); remove it or set XDG_CACHE_HOME to use another location", ErrBadWorkRoot, path, owner, uid)
	}
	return nil
}

// checkOutput fails before minutes are spent bootstrapping when the lock or
// the tarball could not be placed afterwards. The usual cause is a previous
// `sudo frostroot build` in the same directory, which leaves dist/ owned by
// root. dist/ is not created here: a failed build should leave nothing behind.
func checkOutput(dir string) error {
	dirs := []string{dir}
	dist := filepath.Join(dir, "dist")
	if fi, err := os.Stat(dist); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%w: %s exists and is not a directory", ErrUnwritableOutput, dist)
		}
		dirs = append(dirs, dist)
	}
	for _, d := range dirs {
		probe, err := export.CreateTemp(d, ".frostroot.*.tmp")
		if err != nil {
			return fmt.Errorf("%w: %v (was a previous build run with sudo? then remove dist/ and frostroot.lock, or chown them)", ErrUnwritableOutput, err)
		}
		probe.Close()
		os.Remove(probe.Name())
	}
	return nil
}
