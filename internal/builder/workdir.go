package builder

import (
	"fmt"
	"path/filepath"
	"strings"
)

// WorkRoot picks the directory under which each build gets its own work
// directory: $XDG_CACHE_HOME/frostroot, else /var/tmp/frostroot. Never under
// /mnt: on WSL that is a 9p mount of a Windows drive, slow and unreliable for
// the chown and device-node work a bootstrap does.
func WorkRoot(getenv func(string) string) (string, error) {
	root := "/var/tmp/frostroot"
	// The XDG base directory spec says relative paths are invalid; ignore them.
	if cache := getenv("XDG_CACHE_HOME"); filepath.IsAbs(cache) {
		root = filepath.Join(cache, "frostroot")
	}
	if strings.HasPrefix(root, "/mnt/") {
		return "", fmt.Errorf("%w: refusing to build under %s, a 9p mount of a Windows drive under WSL; set XDG_CACHE_HOME to a directory on the Linux filesystem", ErrBadWorkRoot, root)
	}
	return root, nil
}
