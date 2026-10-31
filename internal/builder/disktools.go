package builder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// ErrNoDiskTool means a program a disk build runs on the host is missing.
// Compare with errors.Is; the user can fix it.
var ErrNoDiskTool = errors.New("this host lacks a program disk builds run")

// diskHostTools are what a disk build runs on the host, whoever runs it:
// bubblewrap runs each program of the disk tools tree in a sandbox of its
// own.
var diskHostTools = []hostTool{{"bwrap", "bubblewrap"}}

// unprivilegedDiskHostTools are what a disk build needs besides, run by
// anyone but root: util-linux's unshare, with the helpers that map the
// subordinate ids, removes the tree mmdebstrap made in its user namespace.
var unprivilegedDiskHostTools = slices.Concat([]hostTool{{"unshare", "util-linux"}}, unprivilegedHostTools)

// ToolRunner runs the programs of the disk tools tree and removes the tree
// when the build is done with it. Bubblewrap is the real one; tests use
// fakes.
type ToolRunner interface {
	// Preflight checks the host before any work is done.
	Preflight() error
	// Run runs one program of the tree to completion.
	Run(ctx context.Context, run ToolRun) error
	// RemoveTree removes the tree at dir, whoever owns its files.
	RemoveTree(ctx context.Context, dir string) error
}

// ToolRun is one program of the disk tools tree to run: where the tree and
// the build's work directory are, the program and its arguments, and where
// its output goes.
type ToolRun struct {
	TreeDir string // the tree, mounted read-only
	// WorkDir is bound read-write at /work, which is the working directory,
	// so every path in Args names a file below it as /work/<name>.
	WorkDir string
	Tool    string // the program, found on the tree's own PATH
	Args    []string
	// SourceDateEpoch is the instant the program dates what it writes at,
	// as SOURCE_DATE_EPOCH; 0 leaves the variable unset.
	SourceDateEpoch int64
	Stdout, Stderr  io.Writer // nil discards
}

// toolWorkMount is where a tool finds the build's work directory.
const toolWorkMount = "/work"

// toolEnvironment is the whole environment a tool runs with, besides
// SOURCE_DATE_EPOCH: nothing of the host's, because mke2fs refuses a tar
// whose names are UTF-8 under any other locale, mtools dates what it copies
// by TZ and reads the host's configuration unless told not to, and PATH is
// the tree's.
var toolEnvironment = [][2]string{
	{"PATH", "/usr/sbin:/usr/bin:/sbin:/bin"},
	{"LC_ALL", "C.UTF-8"},
	{"TZ", "UTC"},
	{"MTOOLS_SKIP_CHECK", "1"},
}

// Bubblewrap is the real ToolRunner. Each program runs with bwrap in
// namespaces of its own, with the tree's /usr and /etc read-only, the
// build's work directory at /work, no network, and an environment
// frostroot sets, so that nothing of the host's reaches what it writes.
type Bubblewrap struct {
	RunCommand CommandRunner                // defaults to runInterruptibly
	CurrentUID func() int                   // defaults to os.Getuid
	LookPath   func(string) (string, error) // defaults to exec.LookPath
}

// Preflight checks that every program a disk build runs on the host is
// installed, naming all the missing ones at once with the line that
// installs them.
func (b *Bubblewrap) Preflight() error {
	lookPath := b.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	tools := diskHostTools
	if !b.privileged() {
		tools = slices.Concat(diskHostTools, unprivilegedDiskHostTools)
	}
	var missing, aptPackages []string
	for _, tool := range tools {
		if _, err := lookPath(tool.program); err != nil {
			missing = append(missing, tool.program)
			if !slices.Contains(aptPackages, tool.aptPackage) {
				aptPackages = append(aptPackages, tool.aptPackage)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s; install with: sudo apt install %s", ErrNoDiskTool, strings.Join(missing, ", "), strings.Join(aptPackages, " "))
	}
	return nil
}

// Run runs the program with bwrap. On failure the error names the program
// and repeats the last line it wrote.
func (b *Bubblewrap) Run(ctx context.Context, run ToolRun) error {
	stdout, stderr := run.Stdout, run.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	tail := newTailBuffer(errorTailBytes)
	if err := b.runCommand()(ctx, "bwrap", bubblewrapCommand(run), nil, stdout, io.MultiWriter(stderr, tail)); err != nil {
		if last := lastLine(strings.TrimSpace(tail.String())); last != "" {
			return fmt.Errorf("%s: %w: %s", run.Tool, err, last)
		}
		return fmt.Errorf("%s: %w", run.Tool, err)
	}
	return nil
}

// bubblewrapCommand returns bwrap's arguments for run. The tree's /usr and
// /etc are bound read-only onto bwrap's own root, with the merged-/usr
// links beside them, rather than the tree as /: a mount point under a
// read-only root cannot be made. Every path is an argument of its own,
// never parsed by a shell, and "--" keeps the program's arguments bwrap's
// own apart from them.
func bubblewrapCommand(run ToolRun) []string {
	args := []string{
		"--unshare-all", "--die-with-parent", "--new-session",
		"--ro-bind", filepath.Join(run.TreeDir, "usr"), "/usr",
		"--ro-bind", filepath.Join(run.TreeDir, "etc"), "/etc",
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/sbin", "/sbin",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib64", "/lib64",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
		"--bind", run.WorkDir, toolWorkMount,
		"--chdir", toolWorkMount,
		"--clearenv",
	}
	for _, variable := range toolEnvironment {
		args = append(args, "--setenv", variable[0], variable[1])
	}
	if run.SourceDateEpoch > 0 {
		args = append(args, "--setenv", SourceDateEpochVariable, strconv.FormatInt(run.SourceDateEpoch, 10))
	}
	args = append(args, "--", run.Tool)
	return append(args, run.Args...)
}

// RemoveTree removes the tree with rm, in the user namespace that made it
// when mmdebstrap ran unprivileged: its files then belong to the
// subordinate ids, which only such a namespace can remove, as mmdebstrap's
// manual says. Go code deletes no root filesystem itself.
func (b *Bubblewrap) RemoveTree(ctx context.Context, dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) == string(filepath.Separator) {
		return fmt.Errorf("refusing to remove %q, which is not an absolute path below the root", dir)
	}
	program, args := removeTreeCommand(b.privileged(), dir)
	tail := newTailBuffer(errorTailBytes)
	if err := b.runCommand()(ctx, program, args, nil, io.Discard, tail); err != nil {
		if last := lastLine(strings.TrimSpace(tail.String())); last != "" {
			return fmt.Errorf("removing %s: %w: %s", dir, err, last)
		}
		return fmt.Errorf("removing %s: %w", dir, err)
	}
	return nil
}

// removeTreeCommand returns the program and arguments that remove the tree
// at dir: rm as root, and unshare mapping the subordinate ids around it
// for anyone else.
func removeTreeCommand(privileged bool, dir string) (program string, args []string) {
	if privileged {
		return "rm", []string{"-rf", "--", dir}
	}
	return "unshare", []string{"--map-root-user", "--map-auto", "--", "rm", "-rf", "--", dir}
}

// privileged reports whether the build runs as root, whose mmdebstrap and
// bwrap map no subordinate ids.
func (b *Bubblewrap) privileged() bool {
	currentUID := os.Getuid
	if b.CurrentUID != nil {
		currentUID = b.CurrentUID
	}
	return currentUID() == 0
}

func (b *Bubblewrap) runCommand() CommandRunner {
	if b.RunCommand != nil {
		return b.RunCommand
	}
	return runInterruptibly
}
