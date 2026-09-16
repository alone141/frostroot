package builder

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// Preflight checks the host before any work is done.
func (m *Mmdebstrap) Preflight(spec BootstrapSpec) error {
	lookPath := m.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath("mmdebstrap"); err != nil {
		return fmt.Errorf("%w; install it with: sudo apt install mmdebstrap", ErrNoMmdebstrap)
	}
	return checkKeyring(keyringOf(spec))
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

// command builds the mmdebstrap argument list and extra environment.
func (m *Mmdebstrap) command(spec BootstrapSpec) (args, env []string) {
	mode := m.Mode
	if mode == "" {
		uid := os.Getuid
		if m.Uid != nil {
			uid = m.Uid
		}
		mode = "unshare"
		if uid() == 0 {
			mode = "root"
		}
	}
	args = []string{
		"--mode=" + mode,
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
	// mmdebstrap stages the tarball in TMPDIR; /tmp may be small or tmpfs.
	return args, []string{"TMPDIR=" + spec.WorkDir}
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

// execRun runs a command. Cancelling ctx sends SIGINT, exactly like Ctrl-C in
// a terminal, and then waits for the command to exit however long that takes.
// Never SIGKILL mmdebstrap: in root mode it has proc, sys and dev mounted inside
// the chroot and needs to unmount them. That is also why WaitDelay is unset:
// when it expires Go kills the child.
func execRun(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
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
