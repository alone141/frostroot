package builder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"time"

	"frostroot/internal/deb"
	"frostroot/internal/distro"
	"frostroot/internal/pool"
	"frostroot/internal/recipe"
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
	// One writer for both streams stays one writer, which exec serves with
	// one pipe and one goroutine. Wrapping only stderr made it another, and
	// a buffer given for both then lost what the program wrote to stderr:
	// exec reads stdout straight into a bytes.Buffer, and the read that
	// finds end of file cut the buffer back to what it held when it began.
	if sameWriter(stdout, stderr) {
		combined := io.MultiWriter(stdout, tail)
		stdout, stderr = combined, combined
	} else {
		stderr = io.MultiWriter(stderr, tail)
	}
	if err := b.runCommand()(ctx, "bwrap", bubblewrapCommand(run), nil, stdout, stderr); err != nil {
		if last := lastLine(strings.TrimSpace(tail.String())); last != "" {
			return fmt.Errorf("%s: %w: %s", run.Tool, err, last)
		}
		return fmt.Errorf("%s: %w", run.Tool, err)
	}
	return nil
}

// sameWriter reports whether a and b are one writer. An uncomparable
// writer type would panic on ==, and is two writers either way; so is nil.
func sameWriter(a, b io.Writer) bool {
	typeOfA := reflect.TypeOf(a)
	return typeOfA != nil && typeOfA == reflect.TypeOf(b) && typeOfA.Comparable() && a == b
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

// DiskToolsPackages are the disk tools tree's: what a disk build runs from
// it to make the disk out of the image tarball. Every name is Ubuntu
// 26.04's.
var DiskToolsPackages = []string{
	"e2fsprogs",       // mke2fs -d reads a tar since 1.47.1, and dates what it writes by SOURCE_DATE_EPOCH
	"libarchive13t64", // which mke2fs loads at run time to read the tar, and which nothing pulls in
	"dosfstools",      // mkfs.fat, for the EFI system partition
	"mtools",          // mmd and mcopy, into it
	"fdisk",           // sfdisk, for the partition table
	"qemu-utils",      // qemu-img, for the qcow2
	"tar",             // takes the boot files out of the tarball
	"coreutils",       // dd places each partition
}

// diskToolsRelease is the Ubuntu release the tree is made from, whatever
// the recipe's family or release: its e2fsprogs reads a tar and honors
// SOURCE_DATE_EPOCH, and 24.04's does neither, so no two builds on a 24.04
// host could match.
const diskToolsRelease = "26.04"

// diskToolsVariant is the package set the tree starts from: the essential
// packages, and what DiskToolsPackages pull in.
const diskToolsVariant = "essential"

// What the tree's own work directory, under the build's, holds besides
// mmdebstrap's log and TMPDIR.
const (
	diskToolsDirName   = "disk-tools"
	diskToolsTreeName  = "tree"
	diskToolsStageName = "stage" // what the lock is written from, or checked against
	diskToolsPoolName  = "pool"  // offline: the local repository of the vendored tools
)

// diskToolsTree is the tree a disk build runs its tools in: made by
// mmdebstrap in the build's work directory, recorded in the lock, and
// removed when the build is done with it.
type diskToolsTree struct {
	bootstrapper Bootstrapper
	runner       ToolRunner
	progress     Progress
	release      distro.Release
	mirrorURL    string // replaces the archive's URL, from --mirror; "" for the archive
	trustPEM     []byte // what the tree's apt trusts beyond the host's store
	insecure     bool
	epoch        int64
	offline      *offlinePlan // nil online

	// Set by make.
	workDir  string // the tree's own work directory
	dir      string // the tree, until remove
	packages []recipe.LockPackage
}

// newDiskToolsTree resolves the release the tree is made from.
func newDiskToolsTree(bootstrapper Bootstrapper, runner ToolRunner, options Options, trustPEM []byte, epoch int64, offline *offlinePlan, progress Progress) (*diskToolsTree, error) {
	release, err := distro.Lookup(diskToolsRelease, distro.SupportedArch)
	if err != nil {
		return nil, err
	}
	return &diskToolsTree{
		bootstrapper: bootstrapper,
		runner:       runner,
		progress:     progress,
		release:      release,
		mirrorURL:    options.MirrorURL,
		trustPEM:     trustPEM,
		insecure:     options.Insecure,
		epoch:        epoch,
		offline:      offline,
	}, nil
}

// preflight checks the host for what runs the tools and removes the tree.
func (d *diskToolsTree) preflight() error {
	return d.runner.Preflight()
}

// archiveURL is where the tree's packages come from.
func (d *diskToolsTree) archiveURL() string {
	if d.mirrorURL != "" {
		return d.mirrorURL
	}
	return d.release.ArchiveURL
}

// make has mmdebstrap install the tree under buildDir, as the tools tree
// phase: online from the archive, reading what it installed for the lock;
// offline from a local repository of the vendored tools, checking what it
// installed against the lock.
func (d *diskToolsTree) make(ctx context.Context, buildDir string) error {
	d.progress.Report(ProgressEvent{Phase: PhaseMakeToolsTree, Kind: EventPhaseStarted})
	d.workDir = filepath.Join(buildDir, diskToolsDirName)
	stageDir := filepath.Join(d.workDir, diskToolsStageName)
	// Mode 0755 whatever the umask: mmdebstrap's user namespace is "other"
	// to these directories.
	if err := makeDirectoriesWithMode(stageDir, 0o755); err != nil {
		return err
	}
	dpkgStatusPath := filepath.Join(stageDir, "dpkg-status")
	listsDir := filepath.Join(stageDir, "lists")
	spec := BootstrapSpec{
		Suite:           d.release.Suite,
		SourceLines:     d.release.SourceLines(d.mirrorURL),
		Include:         DiskToolsPackages,
		Variant:         diskToolsVariant,
		CustomizeHooks:  diskToolsHooks(stageDir, dpkgStatusPath, d.offline == nil),
		TreeDir:         filepath.Join(d.workDir, diskToolsTreeName),
		WorkDir:         d.workDir,
		Arch:            distro.SupportedArch, // the host's, which runs the tools
		KeyringPath:     UbuntuArchiveKeyring,
		SourceDateEpoch: d.epoch,
		Progress:        toolsTreeProgress{d.progress},
	}
	if d.offline == nil {
		caInfoPath, err := writeAptCaInfo(d.workDir, d.trustPEM)
		if err != nil {
			return err
		}
		spec.CaInfoPath, spec.Insecure = caInfoPath, d.insecure
	} else {
		// Every locked package by name, from a repository of exactly the
		// lock's files, which frostroot verified itself, as the image's.
		repositoryDir := filepath.Join(d.workDir, diskToolsPoolName)
		if err := d.stageRepository(repositoryDir); err != nil {
			return err
		}
		spec.SourceLines = []string{"deb [trusted=yes] copy://" + repositoryDir + " ./"}
		spec.Include = lockedNames(d.offline.lock.DiskTools)
		spec.Trusted, spec.KeyringPath = true, ""
	}
	// The tree exists from here on, in part at least, whatever mmdebstrap
	// says of it, and remove must find it.
	d.dir = spec.TreeDir
	if err := d.bootstrapper.Run(ctx, spec); err != nil {
		return describeTreeFailure(err)
	}
	installed, err := readDpkgStatus(dpkgStatusPath)
	if err != nil {
		return fmt.Errorf("the disk tools tree: %w", err)
	}
	if d.offline == nil {
		if err := recordChecksums(listsDir, installed, indexOrigins(d.release, d.archiveURL(), nil)); err != nil {
			return fmt.Errorf("the disk tools tree: %w", err)
		}
	} else if details := packageSetDifferences(d.offline.lock.DiskTools, installed, "the disk tools tree"); len(details) > 0 {
		return fmt.Errorf("%w: %s", ErrImageDiffersFromLock, strings.Join(details, "; "))
	}
	d.packages = installed
	d.progress.Report(ProgressEvent{Phase: PhaseMakeToolsTree, Kind: EventPhaseFinished})
	return nil
}

// stageRepository builds the local flat repository an offline tree installs
// from, with a bytes bar under the tools tree phase.
func (d *diskToolsTree) stageRepository(repositoryDir string) error {
	release := deb.FlatRelease{Suite: d.release.Suite, Arch: distro.SupportedArch, Date: time.Now()}
	err := pool.Stage(d.offline.poolDir, d.offline.entriesFor(d.offline.lock.DiskTools), repositoryDir, release, func(doneBytes, totalBytes int64) {
		d.progress.Report(ProgressEvent{Phase: PhaseMakeToolsTree, Kind: EventProgress, Done: doneBytes, Total: totalBytes, Unit: UnitBytes})
	})
	if err != nil {
		return fmt.Errorf("preparing the disk tools' local repository: %w", err)
	}
	return nil
}

// diskToolsHooks are the tree's customize hooks: what the lock is written
// from, or offline checked against, and nothing else, since the tree is run
// from and never shipped.
func diskToolsHooks(stageDir, dpkgStatusPath string, recordForLock bool) []string {
	var hooks []string
	if recordForLock {
		// The indexes apt verified; mmdebstrap deletes them in its cleanup,
		// after the hooks. copy-out puts "lists" inside the destination.
		hooks = append(hooks, "copy-out /var/lib/apt/lists "+shellQuote(stageDir))
	}
	return append(hooks, "download /var/lib/dpkg/status "+shellQuote(dpkgStatusPath))
}

// describeTreeFailure says whose failure it is, and points the command line
// at the tree's own mmdebstrap log rather than the image's.
func describeTreeFailure(err error) error {
	var bootstrapErr *BootstrapError
	if errors.As(err, &bootstrapErr) {
		bootstrapErr.Program = "mmdebstrap"
		bootstrapErr.LogFile = filepath.Join(diskToolsDirName, LogFileName)
	}
	return fmt.Errorf("making the disk tools tree: %w", err)
}

// remove removes the tree make left, if any, however the build ended: an
// interrupted build removes it too.
func (d *diskToolsTree) remove(ctx context.Context) error {
	if d.dir == "" {
		return nil
	}
	dir := d.dir
	d.dir = ""
	if err := d.runner.RemoveTree(context.WithoutCancel(ctx), dir); err != nil {
		return fmt.Errorf("removing the disk tools tree: %w", err)
	}
	return nil
}

// toolsTreeProgress reports mmdebstrap's output of the tree's build as lines
// of the tools tree phase. The phases its parser recognizes are the image's,
// and a display would see the build go backwards.
type toolsTreeProgress struct {
	progress Progress
}

func (p toolsTreeProgress) Report(event ProgressEvent) {
	switch event.Kind {
	case EventLogLine:
		p.progress.Report(ProgressEvent{Phase: PhaseMakeToolsTree, Kind: EventLogLine, Line: event.Line})
	case EventLogFile:
		p.progress.Report(event)
	case EventPhaseStarted, EventProgress, EventPhaseFinished:
	}
}
