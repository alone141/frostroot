package builder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner runs a command to completion, streaming its output.
type Runner func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error

// Mmdebstrap is the real Bootstrapper. mmdebstrap writes the tarball itself,
// from inside its user namespace, which is what keeps ownership, symlink
// targets, hardlinks and file capabilities correct.
type Mmdebstrap struct {
	RunCmd   Runner                       // default execRun
	Mode     string                       // "unshare", "root", or "" to pick from Uid
	Uid      func() int                   // default os.Getuid
	Stderr   io.Writer                    // progress passthrough; nil discards
	LookPath func(string) (string, error) // default exec.LookPath
}

// tailSize is how much of mmdebstrap's output is repeated in a build error.
const tailSize = 4 << 10

// Preflight checks the host before any work is done. spec.WorkDir is the
// work root the build directory will be created in; it may not exist yet.
func (m *Mmdebstrap) Preflight(spec BootstrapSpec) error {
	lookPath := m.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath("mmdebstrap"); err != nil {
		return fmt.Errorf("%w; install it with: sudo apt install mmdebstrap", ErrNoMmdebstrap)
	}
	if err := checkKeyring(keyringOf(spec)); err != nil {
		return err
	}
	if m.mode() == "unshare" && spec.WorkDir != "" {
		return checkReachable(spec.WorkDir)
	}
	return nil
}

// Run bootstraps the image described by spec. Output is streamed to m.Stderr
// as it happens, because a silent five-minute build looks hung and
// mmdebstrap's own messages are the best explanation of a failure (a missing
// user namespace, an unknown package). The last 4 KiB are repeated in the
// error.
func (m *Mmdebstrap) Run(ctx context.Context, spec BootstrapSpec) error {
	if err := checkKeyring(keyringOf(spec)); err != nil {
		return err
	}
	if m.mode() == "unshare" {
		if err := checkReachable(spec.WorkDir); err != nil {
			return err
		}
	}
	// mmdebstrap(1): in unshare mode TMPDIR must be world-writable. Sticky,
	// like /tmp, so other users cannot remove what the build puts there.
	tmp := tmpDirOf(spec)
	if err := os.Mkdir(tmp, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := os.Chmod(tmp, os.ModeSticky|0o777); err != nil {
		return err
	}
	run := m.RunCmd
	if run == nil {
		run = execRun
	}
	progress := m.Stderr
	if progress == nil {
		progress = io.Discard
	}
	tail := newTailBuffer(tailSize)
	out := io.MultiWriter(progress, tail)
	args, env := m.command(spec)
	if err := run(ctx, "mmdebstrap", args, env, out, out); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("mmdebstrap interrupted: %w", ctx.Err())
		}
		return fmt.Errorf("mmdebstrap failed: %w\n--- end of mmdebstrap output ---\n%s", err, strings.TrimRight(tail.String(), "\n"))
	}
	return nil
}

// mode is --mode: as configured, else root for uid 0 and unshare otherwise.
func (m *Mmdebstrap) mode() string {
	if m.Mode != "" {
		return m.Mode
	}
	uid := os.Getuid
	if m.Uid != nil {
		uid = m.Uid
	}
	if uid() == 0 {
		return "root"
	}
	return "unshare"
}

// command builds the mmdebstrap argument list and extra environment.
func (m *Mmdebstrap) command(spec BootstrapSpec) (args, env []string) {
	args = []string{
		"--mode=" + m.mode(),
		"--variant=important",
		"--architectures=" + spec.Arch,
		"--keyring=" + keyringOf(spec),
	}
	if spec.Recommends {
		args = append(args, `--aptopt=Apt::Install-Recommends "true"`)
	}
	if len(spec.Include) > 0 {
		args = append(args, "--include="+strings.Join(spec.Include, ","))
	}
	for _, h := range spec.Hooks {
		args = append(args, "--customize-hook="+h)
	}
	// The source lines carry the components, so --components is not needed.
	args = append(args, spec.Suite, spec.TarPath)
	args = append(args, spec.Sources...)
	// mmdebstrap builds the rootfs in TMPDIR before packing it; /tmp may be
	// small or tmpfs.
	return args, []string{"TMPDIR=" + tmpDirOf(spec)}
}

func tmpDirOf(spec BootstrapSpec) string { return filepath.Join(spec.WorkDir, "tmp") }

// checkReachable reports whether mmdebstrap's user namespace can reach dir.
// In unshare mode the namespace's root is a subordinate uid, so it is "other"
// to the user's own files: every existing ancestor must be world-executable.
// Components that do not exist yet are created 0755 by the builder. Home
// directories are 0750 on current Ubuntu, which is why the default work root
// is /var/tmp/frostroot and not ~/.cache.
func checkReachable(dir string) error {
	for p := filepath.Clean(dir); ; p = filepath.Dir(p) {
		fi, err := os.Stat(p)
		switch {
		case err == nil && fi.Mode().Perm()&0o001 == 0:
			return fmt.Errorf("%w: mmdebstrap runs in a user namespace that cannot enter %s (mode %04o), so it cannot use %s; set XDG_CACHE_HOME to a directory whose parents are all world-executable, or unset it to use /var/tmp/frostroot",
				ErrBadWorkRoot, p, fi.Mode().Perm(), dir)
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return err
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}

func keyringOf(spec BootstrapSpec) string {
	if spec.Keyring != "" {
		return spec.Keyring
	}
	return UbuntuKeyring
}

func checkKeyring(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%w at %s; install it with: sudo apt install ubuntu-keyring", ErrNoKeyring, path)
	}
	return nil
}

// execRun runs a command. Cancelling ctx interrupts it like Ctrl-C in a
// terminal (see interruptOnCancel) and then waits for it to exit however long
// that takes. Never SIGKILL mmdebstrap: in root mode it has proc, sys and dev
// mounted inside the chroot and needs to unmount them. That is also why
// WaitDelay is unset: when it expires Go kills the child.
func execRun(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	interruptOnCancel(cmd)
	return cmd.Run()
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max       int
	buf       []byte
	truncated bool
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0:0], t.buf[over:]...)
		t.truncated = true
	}
	return len(p), nil
}

// String returns the kept output, starting at a line boundary once older
// output has been dropped.
func (t *tailBuffer) String() string {
	b := t.buf
	if t.truncated {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return string(b)
}
