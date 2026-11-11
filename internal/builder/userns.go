package builder

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNoUserNamespaces means the host's kernel makes no user namespace for a
// build run without root: WSL 1, or a kernel or container that switched
// them off.
var ErrNoUserNamespaces = errors.New("this host makes no user namespaces")

// The kernel's files that say whether it makes user namespaces at all.
const (
	kernelReleasePath = "/proc/sys/kernel/osrelease"
	// maxUserNamespacesPath holds 0 where they are switched off.
	maxUserNamespacesPath = "/proc/sys/user/max_user_namespaces"
	// unprivilegedCloneSwitchPath exists only in Debian's kernels, and holds 0
	// where only root may make one.
	unprivilegedCloneSwitchPath = "/proc/sys/kernel/unprivileged_userns_clone"
)

// checkUserNamespaces refuses a host whose kernel cannot give a build run
// without root the user namespace mmdebstrap, mkosi and bwrap run in. They
// would each fail at their first step with a message of their own and exit
// 2; this says what to do instead. A file that cannot be read proves
// nothing and passes, so a kernel this does not know is left to the tools.
func checkUserNamespaces(readFile func(string) ([]byte, error)) error {
	read := func(path string) (string, bool) {
		content, err := readFile(path)
		return strings.TrimSpace(string(content)), err == nil
	}
	// WSL 1 emulates Linux's system calls and has no user namespaces; its
	// release ends in "-Microsoft", with the capital, where WSL 2's is
	// "...-microsoft-standard-WSL2".
	if release, ok := read(kernelReleasePath); ok && strings.HasSuffix(release, "-Microsoft") {
		return fmt.Errorf("%w: this is WSL 1, which has none; convert the distribution to WSL 2, which needs Windows 10 version 1903 or newer, from Windows with: wsl --set-version DISTRIBUTION 2",
			ErrNoUserNamespaces)
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
