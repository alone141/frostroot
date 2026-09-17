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
	"strconv"
	"strings"
)

// CommandRunner runs a program to completion, streaming its output. Tests
// replace it to record the command line instead of running mmdebstrap.
type CommandRunner func(ctx context.Context, program string, args, environment []string, stdout, stderr io.Writer) error

// Mmdebstrap is the real Bootstrapper. mmdebstrap writes the tarball itself,
// from inside its user namespace, which is what keeps ownership, symlink
// targets, hardlinks and file capabilities correct.
type Mmdebstrap struct {
	RunCommand CommandRunner                // defaults to runInterruptibly
	Mode       string                       // "unshare", "root", or "" to choose from the current uid
	CurrentUID func() int                   // defaults to os.Getuid
	LookPath   func(string) (string, error) // defaults to exec.LookPath
}

// errorTailBytes is how much of mmdebstrap's output is repeated in a build
// error.
const errorTailBytes = 4 << 10

// LogFileName is the file in the work directory that receives mmdebstrap's
// complete output.
const LogFileName = "mmdebstrap.log"

// Preflight checks the host before any work is done. spec.WorkDir is the work
// root the build directory will be created in; it need not exist yet.
func (m *Mmdebstrap) Preflight(spec BootstrapSpec) error {
	lookPath := m.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath("mmdebstrap"); err != nil {
		return fmt.Errorf("%w; install it with: sudo apt install mmdebstrap", ErrNoMmdebstrap)
	}
	if err := checkKeyring(spec); err != nil {
		return err
	}
	if m.bootstrapMode() == "unshare" && spec.WorkDir != "" {
		return checkReachableFromUserNamespace(spec.WorkDir)
	}
	return nil
}

// Run bootstraps the image described by spec. mmdebstrap's output goes three
// ways as it happens: parsed into phases and progress for spec.Progress,
// written whole to LogFileName in the work directory, and kept in a tail that
// is repeated in the error, because mmdebstrap's own messages are the best
// explanation of a failure such as a missing user namespace or an unknown
// package.
func (m *Mmdebstrap) Run(ctx context.Context, spec BootstrapSpec) error {
	if err := checkKeyring(spec); err != nil {
		return err
	}
	if m.bootstrapMode() == "unshare" {
		if err := checkReachableFromUserNamespace(spec.WorkDir); err != nil {
			return err
		}
	}
	// mmdebstrap(1): in unshare mode TMPDIR must be world-writable. Sticky,
	// like /tmp, so other users cannot remove what the build puts there.
	temporaryDir := temporaryDirFor(spec)
	if err := os.Mkdir(temporaryDir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := os.Chmod(temporaryDir, os.ModeSticky|0o777); err != nil {
		return err
	}

	runCommand := m.RunCommand
	if runCommand == nil {
		runCommand = runInterruptibly
	}
	logPath := filepath.Join(spec.WorkDir, LogFileName)
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("creating the mmdebstrap log: %w", err)
	}
	progressOrDiscard(spec.Progress).Report(ProgressEvent{Kind: EventLogFile, Line: logPath})
	parser := newProgressParser(spec.Progress)
	outputTail := newTailBuffer(errorTailBytes)
	combinedOutput := io.MultiWriter(logFile, parser, outputTail)
	args, environment := m.commandLine(spec)
	runErr := runCommand(ctx, "mmdebstrap", args, environment, combinedOutput, combinedOutput)
	parser.flush()
	if err := logFile.Close(); err != nil && runErr == nil {
		return fmt.Errorf("writing the mmdebstrap log: %w", err)
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("mmdebstrap interrupted: %w", ctx.Err())
		}
		return fmt.Errorf("mmdebstrap failed: %w\n--- last lines of mmdebstrap output ---\n%s", runErr, strings.TrimRight(outputTail.String(), "\n"))
	}
	return nil
}

// bootstrapMode returns the value for mmdebstrap's --mode: as configured,
// otherwise root for uid 0 and unshare for everyone else.
func (m *Mmdebstrap) bootstrapMode() string {
	if m.Mode != "" {
		return m.Mode
	}
	currentUID := os.Getuid
	if m.CurrentUID != nil {
		currentUID = m.CurrentUID
	}
	if currentUID() == 0 {
		return "root"
	}
	return "unshare"
}

// commandLine returns the mmdebstrap arguments for spec and the variables to
// add to its environment.
func (m *Mmdebstrap) commandLine(spec BootstrapSpec) (args, environment []string) {
	args = []string{
		// Without a terminal mmdebstrap prints only its phase messages;
		// --verbose adds apt's and dpkg's output, which is what the progress
		// parser measures.
		"--verbose",
		"--mode=" + m.bootstrapMode(),
		"--variant=important",
		"--architectures=" + spec.Arch,
	}
	if !spec.Trusted {
		args = append(args, "--keyring="+keyringPath(spec))
	}
	if spec.InstallRecommends {
		args = append(args, `--aptopt=Apt::Install-Recommends "true"`)
	}
	if len(spec.Include) > 0 {
		args = append(args, "--include="+strings.Join(spec.Include, ","))
	}
	for _, hook := range spec.CustomizeHooks {
		args = append(args, "--customize-hook="+hook)
	}
	// The source lines carry the components, so --components is not needed.
	args = append(args, spec.Suite, spec.TarballPath)
	args = append(args, spec.SourceLines...)
	// mmdebstrap assembles the root filesystem in TMPDIR before packing it,
	// and /tmp may be small or a tmpfs.
	environment = []string{"TMPDIR=" + temporaryDirFor(spec)}
	if spec.SourceDateEpoch > 0 {
		// The reproducible-builds convention. mmdebstrap dates no tarball
		// entry later than this, sorts the entries, writes a gzip header
		// without a timestamp and removes the files that would carry the
		// build time; shadow dates /etc/shadow by it too.
		environment = append(environment, "SOURCE_DATE_EPOCH="+strconv.FormatInt(spec.SourceDateEpoch, 10))
	}
	return args, environment
}

// temporaryDirFor returns the TMPDIR mmdebstrap uses for spec.
func temporaryDirFor(spec BootstrapSpec) string {
	return filepath.Join(spec.WorkDir, "tmp")
}

// checkReachableFromUserNamespace reports whether mmdebstrap's user namespace
// can reach dir. In unshare mode the namespace's root is a subordinate uid, so
// it is "other" to the user's own files: every existing ancestor must be
// world-executable. Components that do not exist yet are created 0755 by the
// builder. Home directories are 0750 on current Ubuntu, which is why the
// default work root is under /var/tmp and not ~/.cache.
func checkReachableFromUserNamespace(dir string) error {
	for ancestor := filepath.Clean(dir); ; ancestor = filepath.Dir(ancestor) {
		ancestorInfo, err := os.Stat(ancestor)
		switch {
		case err == nil && ancestorInfo.Mode().Perm()&0o001 == 0:
			return fmt.Errorf("%w: mmdebstrap runs in a user namespace that cannot enter %s (mode %04o), so it cannot use %s; set XDG_CACHE_HOME to a directory whose parents are all world-executable, or unset it to use the default under /var/tmp",
				ErrBadWorkRoot, ancestor, ancestorInfo.Mode().Perm(), dir)
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return err
		}
		if ancestor == filepath.Dir(ancestor) {
			return nil
		}
	}
}

// keyringPath returns the keyring for spec, defaulting to the Ubuntu archive
// keyring.
func keyringPath(spec BootstrapSpec) string {
	if spec.KeyringPath != "" {
		return spec.KeyringPath
	}
	return UbuntuArchiveKeyring
}

// checkKeyring returns ErrNoKeyring, with an install hint, when the spec
// needs a keyring that does not exist. A trusted source needs none.
func checkKeyring(spec BootstrapSpec) error {
	if spec.Trusted {
		return nil
	}
	path := keyringPath(spec)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%w at %s; install it with: sudo apt install ubuntu-keyring", ErrNoKeyring, path)
	}
	return nil
}

// runInterruptibly runs a program. Canceling ctx interrupts it the way Ctrl-C
// in a terminal does (see interruptOnCancel) and then waits for it to exit,
// however long that takes. mmdebstrap is never sent SIGKILL: in root mode it
// has proc, sys and dev mounted inside the chroot and must unmount them. That
// is also why WaitDelay stays unset, since Go kills the child when it expires.
func runInterruptibly(ctx context.Context, program string, args, environment []string, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, program, args...)
	command.Env = append(os.Environ(), environment...)
	command.Stdout = stdout
	command.Stderr = stderr
	interruptOnCancel(command)
	return command.Run()
}

// tailBuffer is an io.Writer that keeps only the last capacity bytes written
// to it.
type tailBuffer struct {
	capacity  int
	kept      []byte
	truncated bool // older output has been dropped
}

func newTailBuffer(capacity int) *tailBuffer { return &tailBuffer{capacity: capacity} }

// Write keeps the end of data and never fails.
func (b *tailBuffer) Write(data []byte) (int, error) {
	b.kept = append(b.kept, data...)
	if excess := len(b.kept) - b.capacity; excess > 0 {
		b.kept = append([]byte(nil), b.kept[excess:]...)
		b.truncated = true
	}
	return len(data), nil
}

// String returns the kept output, starting at a line boundary once older
// output has been dropped.
func (b *tailBuffer) String() string {
	kept := b.kept
	if b.truncated {
		if newline := bytes.IndexByte(kept, '\n'); newline >= 0 {
			kept = kept[newline+1:]
		}
	}
	return string(kept)
}
