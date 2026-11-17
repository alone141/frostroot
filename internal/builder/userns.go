package builder

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNoUserNamespaces means the host's kernel makes no user namespace for a
// build run without root: a kernel or container that switched them off.
var ErrNoUserNamespaces = errors.New("this host makes no user namespaces")

// ErrWSL1 means the host is WSL 1, which cannot build an image as root or
// as anyone else.
var ErrWSL1 = errors.New("WSL 1 cannot build an image")

// The kernel's files that say what it is and whether it makes user
// namespaces at all.
const (
	kernelReleasePath = "/proc/sys/kernel/osrelease"
	// maxUserNamespacesPath holds 0 where they are switched off.
	maxUserNamespacesPath = "/proc/sys/user/max_user_namespaces"
	// unprivilegedCloneSwitchPath exists only in Debian's kernels, and holds 0
	// where only root may make one.
	unprivilegedCloneSwitchPath = "/proc/sys/kernel/unprivileged_userns_clone"
)

// isWSL1 reports whether the host is WSL 1. WSL 1 emulates Linux's system
// calls; its kernel release ends in "-Microsoft", with the capital, where
// WSL 2's is "...-microsoft-standard-WSL2". A release that cannot be read
// is not WSL 1.
func isWSL1(readFile func(string) ([]byte, error)) bool {
	release, err := readFile(kernelReleasePath)
	return err == nil && strings.HasSuffix(strings.TrimSpace(string(release)), "-Microsoft")
}

// checkNotWSL1 refuses WSL 1, for root too. It has no user namespaces, and
// as root the build failed minutes in, in systemd's maintainer script or
// in bubblewrap. The machines that run it often cannot convert to WSL 2,
// so the message says first where an image can be built.
func checkNotWSL1(readFile func(string) ([]byte, error)) error {
	if !isWSL1(readFile) {
		return nil
	}
	return fmt.Errorf("%w; build on Linux or in a WSL 2 distribution and bring the image here, or, on Windows 10 version 1903 or newer, convert this distribution to WSL 2 from Windows with: wsl --set-version DISTRIBUTION 2",
		ErrWSL1)
}

// checkUserNamespaces refuses a host whose kernel cannot give a build run
// without root the user namespace mmdebstrap, mkosi and bwrap run in. They
// would each fail at their first step with a message of their own and exit
// 2; this says what to do instead. A file that cannot be read proves
// nothing and passes, so a kernel this does not know is left to the tools.
// WSL 1 has none either, and checkNotWSL1 has refused it before this runs.
func checkUserNamespaces(readFile func(string) ([]byte, error)) error {
	read := func(path string) (string, bool) {
		content, err := readFile(path)
		return strings.TrimSpace(string(content)), err == nil
	}
	if limit, ok := read(maxUserNamespacesPath); ok && limit == "0" {
		return fmt.Errorf("%w: user.max_user_namespaces is 0; build as root with sudo frostroot build, or allow them: sudo sysctl -w user.max_user_namespaces=10000",
			ErrNoUserNamespaces)
	}
	if allowed, ok := read(unprivilegedCloneSwitchPath); ok && allowed == "0" {
		return fmt.Errorf("%w: kernel.unprivileged_userns_clone is 0; build as root with sudo frostroot build, or allow them: sudo sysctl -w kernel.unprivileged_userns_clone=1",
			ErrNoUserNamespaces)
	}
	return nil
}
