package builder

import (
	"bytes"
	"fmt"
	"strings"
)

// mmdebstrap mounts /proc, /sys and /dev in the chroot while it installs.
// When it decides it cannot, it prints a warning and installs without them;
// the maintainer scripts that need them then fail without failing anything,
// and the image lacks what they would have made. Built as root without
// CAP_SYS_ADMIN, a 24.04 image lacked the eleven entries systemd-tmpfiles
// makes at install time, /root/.ssh and /var/lib/private among them, had
// /etc/credstore at 0755 rather than 0700, and /var/log/journal owned by
// root rather than the systemd-journal group; a 26.04 image built without
// mount also had a stub hwdb.bin and no systemd catalog. mmdebstrap exited
// 0 both times.

// isMountRefusal reports whether line is one of the warnings with which
// mmdebstrap says it will install with nothing mounted. It prints one for
// each reason it has, before it fetches anything, worded the same in
// mmdebstrap 1.4 and 1.5: mount is missing or does not run, which it checks
// in root and unshare mode; and, in root mode, CAP_SYS_ADMIN is missing
// from the effective or the bounding set, or a mount namespace cannot be
// made, as for root in a container that is not privileged. The two lines
// that follow later, whatever the reason, catch a reason a newer
// mmdebstrap words differently.
func isMountRefusal(line string) bool {
	switch {
	case line == "W: cannot execute mount",
		strings.HasPrefix(line, "W: cannot mount because "),
		line == "W: skipping mount sysfs",
		line == "W: skipping mount proc":
		return true
	}
	return false
}

// mountWatch is an io.Writer that reads mmdebstrap's output as it is
// written, beside the log and the progress parser, and calls stop at the
// first line isMountRefusal recognizes. stop cancels the run, so such a
// build fails within seconds rather than after producing an image nobody
// should import.
type mountWatch struct {
	stop        func()
	partialLine []byte
	refusal     string // the first line that refused, once seen
}

func newMountWatch(stop func()) *mountWatch { return &mountWatch{stop: stop} }

// Write consumes bootstrap output. It never returns an error: the watch
// stops a build through stop, never by failing a write.
func (w *mountWatch) Write(data []byte) (int, error) {
	if w.refusal != "" {
		return len(data), nil
	}
	w.partialLine = append(w.partialLine, data...)
	for w.refusal == "" {
		newline := bytes.IndexByte(w.partialLine, '\n')
		if newline < 0 {
			break
		}
		line := string(w.partialLine[:newline])
		w.partialLine = w.partialLine[newline+1:]
		w.check(strings.TrimSuffix(line, "\r"))
	}
	return len(data), nil
}

// flush checks a final line that had no newline.
func (w *mountWatch) flush() {
	if len(w.partialLine) > 0 && w.refusal == "" {
		w.check(strings.TrimSuffix(string(w.partialLine), "\r"))
	}
	w.partialLine = nil
}

func (w *mountWatch) check(line string) {
	if !isMountRefusal(line) {
		return
	}
	w.refusal = line
	w.partialLine = nil
	w.stop()
}

// mountRefusalError explains the warning with which mmdebstrap refused to
// mount, and what would let it.
func mountRefusalError(warning string) error {
	var remedy string
	switch {
	case warning == "W: cannot execute mount":
		remedy = "mmdebstrap runs mount --version to decide; if mount is missing or broken, reinstall it with: sudo apt install --reinstall mount"
	case strings.HasPrefix(warning, "W: cannot mount because "):
		remedy = "as root, mounting needs CAP_SYS_ADMIN, which a container lacks unless it is started with it, as docker run --privileged does; or build as a normal user, which mounts inside a user namespace instead"
	default:
		remedy = "the lines before it in " + LogFileName + " say why"
	}
	return fmt.Errorf("%w: it said %q and would have installed without them, which makes an incomplete image; %s", ErrCannotMount, warning, remedy)
}
