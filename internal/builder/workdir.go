package builder

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"frostroot/internal/export"
)

// DefaultWorkRoot returns where builds work when XDG_CACHE_HOME is unset. It
// is per user because /var/tmp is shared: a root-owned /var/tmp/frostroot left
// by one `sudo frostroot build` would block every later unprivileged build.
func DefaultWorkRoot(uid int) string {
	return "/var/tmp/frostroot-" + strconv.Itoa(uid)
}

// WorkRoot picks the directory under which each build gets its own work
// directory: $XDG_CACHE_HOME/frostroot, else DefaultWorkRoot. Never under
// /mnt: on WSL that is a 9p mount of a Windows drive, slow and unreliable for
// the chown and device-node work a bootstrap does.
func WorkRoot(getenv func(string) string, uid int) (string, error) {
	workRoot := DefaultWorkRoot(uid)
	// The XDG base directory specification says relative paths are invalid,
	// so they are ignored.
	if cacheHome := getenv("XDG_CACHE_HOME"); filepath.IsAbs(cacheHome) {
		workRoot = filepath.Join(cacheHome, "frostroot")
	}
	if strings.HasPrefix(workRoot, "/mnt/") {
		return "", fmt.Errorf("%w: refusing to build under %s, a 9p mount of a Windows drive under WSL; set XDG_CACHE_HOME to a directory on the Linux filesystem", ErrBadWorkRoot, workRoot)
	}
	return workRoot, nil
}

// prepareWorkRoot creates workRoot, mode 0755 whatever the umask so that
// mmdebstrap's user namespace can enter it, and checks that it is a directory
// owned by uid. A work root owned by someone else is either left over from
// their build, which could not be written into anyway, or planted in shared
// /var/tmp, and must not be used. A symbolic link the user owns is followed:
// pointing the work root at a bigger disk is legitimate.
func prepareWorkRoot(workRoot string, uid int) error {
	if err := makeDirectoriesWithMode(workRoot, 0o755); err != nil {
		return fmt.Errorf("%w: cannot create %s: %w; set XDG_CACHE_HOME to use another location", ErrBadWorkRoot, workRoot, err)
	}
	workRootInfo, err := os.Lstat(workRoot)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadWorkRoot, err)
	}
	if workRootInfo.Mode()&os.ModeSymlink != 0 {
		if err := checkOwnedBy(workRoot, workRootInfo, uid); err != nil {
			return err
		}
		if workRootInfo, err = os.Stat(workRoot); err != nil {
			return fmt.Errorf("%w: %w", ErrBadWorkRoot, err)
		}
	}
	if !workRootInfo.IsDir() {
		return fmt.Errorf("%w: %s is not a directory; remove it or set XDG_CACHE_HOME to use another location", ErrBadWorkRoot, workRoot)
	}
	return checkOwnedBy(workRoot, workRootInfo, uid)
}

// checkOwnedBy returns ErrBadWorkRoot unless the file described by info is
// owned by uid.
func checkOwnedBy(path string, info os.FileInfo, uid int) error {
	owner, ownerKnown := fileOwner(info)
	if ownerKnown && owner != uid {
		return fmt.Errorf("%w: %s is owned by uid %d, not by you (uid %d); remove it or set XDG_CACHE_HOME to use another location", ErrBadWorkRoot, path, owner, uid)
	}
	return nil
}

// checkOutputWritable fails before minutes are spent bootstrapping when the
// lock or the tarball could not be placed afterwards. The usual cause is an
// earlier `sudo frostroot build` in the same directory, which leaves dist/
// owned by root. dist/ is not created here: a failed build leaves nothing
// behind.
func checkOutputWritable(recipeDir string) error {
	directoriesToProbe := []string{recipeDir}
	distDir := filepath.Join(recipeDir, "dist")
	if distInfo, err := os.Stat(distDir); err == nil {
		if !distInfo.IsDir() {
			return fmt.Errorf("%w: %s exists and is not a directory", ErrUnwritableOutput, distDir)
		}
		directoriesToProbe = append(directoriesToProbe, distDir)
	}
	for _, directory := range directoriesToProbe {
		probe, err := export.CreateTemp(directory, ".frostroot.*.tmp")
		if err != nil {
			return fmt.Errorf("%w: %w (was a previous build run with sudo? then remove dist/ and frostroot.lock, or chown them)", ErrUnwritableOutput, err)
		}
		if err := errors.Join(probe.Close(), os.Remove(probe.Name())); err != nil {
			return fmt.Errorf("%w: %w", ErrUnwritableOutput, err)
		}
	}
	return nil
}
