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
	"slices"
	"strconv"
	"strings"
)

// Errors for a host that cannot build a Fedora image, which the user can fix.
// Compare with errors.Is.
var (
	// ErrNoFedoraTool means a program a Fedora build runs on the host is
	// missing, or is installed and does not run.
	ErrNoFedoraTool = errors.New("this host lacks a program Fedora builds run")
	// ErrMkosiVersion means the installed mkosi is not a version frostroot
	// knows how to drive: its settings and command line change between
	// versions, and so do the bytes it writes.
	ErrMkosiVersion = errors.New("mkosi version frostroot does not know")
	// ErrFedoraOption means the build was given an option a Fedora build
	// cannot use.
	ErrFedoraOption = errors.New("not for a Fedora build")
	// ErrUserNamespacesRestricted means the host keeps an unprivileged
	// mkosi out of the user namespace it makes.
	ErrUserNamespacesRestricted = errors.New("this host keeps mkosi out of its user namespace")
)

// userNamespaceRestriction is where Ubuntu's kernels say whether AppArmor
// keeps a program that has no profile of its own from using the user
// namespaces it makes: 1 since Ubuntu 23.10. mmdebstrap has such a profile,
// and mkosi does not. WSL runs Microsoft's kernel, which has no such switch.
const userNamespaceRestriction = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"

// KnownMkosiVersions are the mkosi versions frostroot drives, oldest first:
// Ubuntu 24.04's, which the Fedora spike measured.
var KnownMkosiVersions = []string{"20.2"}

// hostTool is a program a build runs on the host, and the Ubuntu package
// that installs it.
type hostTool struct {
	program, aptPackage string
}

// fedoraHostTools are what a Fedora build runs on the host, whoever runs it.
var fedoraHostTools = []hostTool{
	{"mkosi", "mkosi"},
	// dnf makes the tools tree, which then makes the image with dnf5.
	{"dnf", "dnf"},
	// mkosi 20.2 runs rpm itself for any rpm-based image, and Ubuntu's dnf
	// does not pull it in.
	{"rpm", "rpm"},
	// Offline, the vendored packages need their index before any tools tree
	// exists to make it.
	{"createrepo_c", "createrepo-c"},
	{"bwrap", "bubblewrap"},
	// mkosi's own gzip dates what it writes; the tarball is compressed
	// afterwards with gzip -n.
	{"gzip", "gzip"},
}

// unprivilegedHostTools are what mkosi needs besides, run by anyone but root:
// the helpers that map the subordinate ids into its user namespace.
var unprivilegedHostTools = []hostTool{
	{"newuidmap", "uidmap"},
	{"newgidmap", "uidmap"},
}

// Mkosi builds Fedora images. mkosi makes the tools tree with the host's
// dnf, then the image with the tools tree's dnf5, and writes the tar from
// inside its user namespace, which is what keeps ownership, hardlinks and
// file capabilities correct.
type Mkosi struct {
	RunCommand CommandRunner                // defaults to runInterruptibly
	CurrentUID func() int                   // defaults to os.Getuid
	LookPath   func(string) (string, error) // defaults to exec.LookPath
	ReadFile   func(string) ([]byte, error) // defaults to os.ReadFile
}

// FedoraSpec is everything the Mkosi bootstrapper needs to build one image.
// The configuration directories hold what frostroot wrote for each of
// mkosi's two builds: mkosi.conf, the package manager tree, the scripts,
// and the files the scripts install.
type FedoraSpec struct {
	WorkDir        string // the build directory; during Preflight, the work root
	ToolsConfigDir string // mkosi's configuration for the tools tree
	ImageConfigDir string // mkosi's configuration for the image
	// CacheDir is mkosi's package cache: online, kept under the work root
	// between builds; offline, the build's own, removed with the tools tree.
	CacheDir       string
	ToolsOutputDir string // where mkosi writes the tools tree, which is removed when the build ends
	// ToolsRecordPath is where the record of the tools tree's packages is
	// left: mkosi's clean removes everything in its output directory.
	ToolsRecordPath string
	ImageOutputDir  string // where mkosi writes the image's tar, and the records of its packages
	// SourceDateEpoch is the instant every timestamp in the image is
	// clamped to, and what rpm dates each package's install from.
	SourceDateEpoch int64
	TarballPath     string   // where the gzip-compressed image goes
	Progress        Progress // receives phases and output lines; nil discards them
	// Offline builds install from LocalRepositories, the vendored packages
	// staged one directory per repository, which Run indexes first. mkosi
	// binds LocalMirror, the directory that holds them all, into its
	// sandbox, where the repository files find them.
	Offline           bool
	LocalMirror       string
	LocalRepositories []string
}

// Preflight checks the host before any work is done: every program a
// Fedora build runs, named all at once with the line that installs them,
// the mkosi version, a dnf that runs, and, unprivileged, a kernel that lets
// mkosi use its user namespace and a work root that namespace can enter.
// spec.WorkDir need not exist yet.
func (m *Mkosi) Preflight(ctx context.Context, spec FedoraSpec) error {
	lookPath := m.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	tools := fedoraHostTools
	if !m.privileged() {
		tools = slices.Concat(fedoraHostTools, unprivilegedHostTools)
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
		return fmt.Errorf("%w: %s; install with: sudo apt install %s", ErrNoFedoraTool, strings.Join(missing, ", "), strings.Join(aptPackages, " "))
	}
	versionOutput, err := m.output(ctx, "mkosi", "--version")
	if err != nil {
		return fmt.Errorf("%w: mkosi --version: %w", ErrNoFedoraTool, err)
	}
	if version := mkosiVersion(versionOutput); !slices.Contains(KnownMkosiVersions, version) {
		return fmt.Errorf("%w: this host has mkosi %q, and frostroot builds Fedora with %s; Ubuntu 24.04's mkosi is 20.2",
			ErrMkosiVersion, version, strings.Join(KnownMkosiVersions, " or "))
	}
	// dnf is a Python program: installed is not the same as working.
	if _, err := m.output(ctx, "dnf", "--version"); err != nil {
		return fmt.Errorf("%w: dnf is installed but does not run: %w", ErrNoFedoraTool, err)
	}
	if m.privileged() {
		return nil
	}
	readFile := m.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	// mkosi would fail inside its namespace, with a traceback.
	if restricted, err := readFile(userNamespaceRestriction); err == nil && strings.TrimSpace(string(restricted)) == "1" {
		return fmt.Errorf("%w: AppArmor lets only a program with a profile of its own use the user namespace it makes (kernel.apparmor_restrict_unprivileged_userns = 1), and mkosi has none; build as root with sudo frostroot build, or lift the restriction: sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0",
			ErrUserNamespacesRestricted)
	}
	if spec.WorkDir != "" {
		return checkReachableFromUserNamespace("mkosi", spec.WorkDir)
	}
	return nil
}

// privileged reports whether mkosi runs as root, which needs no subordinate
// ids.
func (m *Mkosi) privileged() bool {
	currentUID := os.Getuid
	if m.CurrentUID != nil {
		currentUID = m.CurrentUID
	}
	return currentUID() == 0
}

// output runs a quick host program and returns what it printed; on failure
// the error carries the end of its standard error.
func (m *Mkosi) output(ctx context.Context, program string, args ...string) (string, error) {
	runCommand := m.RunCommand
	if runCommand == nil {
		runCommand = runInterruptibly
	}
	var stdout bytes.Buffer
	stderr := newTailBuffer(errorTailBytes)
	if err := runCommand(ctx, program, args, nil, &stdout, stderr); err != nil {
		if tail := strings.TrimSpace(stderr.String()); tail != "" {
			return "", fmt.Errorf("%w: %s", err, lastLine(tail))
		}
		return "", err
	}
	return stdout.String(), nil
}

// mkosiVersion returns the version in `mkosi --version`'s output, "mkosi
// 20.2", or the output itself, trimmed, when it is not of that form.
func mkosiVersion(output string) string {
	fields := strings.Fields(output)
	if len(fields) == 2 && fields[0] == "mkosi" {
		return fields[1]
	}
	return strings.TrimSpace(output)
}

// lastLine returns the last line of text.
func lastLine(text string) string {
	return text[strings.LastIndexByte(text, '\n')+1:]
}

// MkosiLogFileName is the file in the work directory that receives the
// complete output of mkosi's two builds and of gzip.
const MkosiLogFileName = "mkosi.log"

// Run makes the tools tree, then the image, then compresses it with gzip -n
// into spec.TarballPath; offline, it indexes the local repositories first.
// Every program's output goes to MkosiLogFileName and is parsed into phases
// for spec.Progress; the end of it is repeated in the error, because mkosi's
// and dnf's own messages are the best explanation of a failure.
func (m *Mkosi) Run(ctx context.Context, spec FedoraSpec) (runErr error) {
	for _, dir := range []string{spec.CacheDir, spec.ToolsOutputDir, spec.ImageOutputDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	logPath := filepath.Join(spec.WorkDir, MkosiLogFileName)
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer func() {
		if err := logFile.Close(); err != nil && runErr == nil {
			runErr = err
		}
	}()
	progress := progressOrDiscard(spec.Progress)
	progress.Report(ProgressEvent{Kind: EventLogFile, Line: logPath})
	parser := &mkosiProgress{progress: progress, phases: FedoraPhases(spec.Offline), phase: PhaseMakeToolsTree}
	tail := newTailBuffer(errorTailBytes)
	lines := &lineSplitter{emit: parser.line}
	output := io.MultiWriter(logFile, tail, lines)
	fail := func(what string, err error) error {
		lines.flush()
		return &BootstrapError{Err: fmt.Errorf("%s: %w", what, err), Tail: tail.String(), Program: "mkosi", LogFile: MkosiLogFileName}
	}
	// The tools tree belongs to the subordinate ids an unprivileged mkosi
	// maps, so the user cannot remove it, and frostroot does not delete a
	// root filesystem itself: mkosi removes it, in its own user namespace,
	// however the build ends. It is no evidence of a failure; the log is.
	// An offline build's package cache is its own, and belongs to them
	// too: -ff removes it with the tree.
	defer func() {
		clean := []string{"--directory=" + spec.ToolsConfigDir, "--output-dir=" + spec.ToolsOutputDir, "-f", "clean"}
		if spec.Offline {
			clean = []string{"--directory=" + spec.ToolsConfigDir, "--output-dir=" + spec.ToolsOutputDir, "--cache-dir=" + spec.CacheDir, "-ff", "clean"}
		}
		cleanErr := m.mkosi(context.WithoutCancel(ctx), output, clean)
		if cleanErr != nil && runErr == nil {
			runErr = fmt.Errorf("removing the tools tree: %w", cleanErr)
		}
	}()

	progress.Report(ProgressEvent{Phase: PhaseMakeToolsTree, Kind: EventPhaseStarted})
	// Offline, mkosi binds the local mirror into its sandbox, where the
	// repository files point, and nothing else of the host's.
	var localMirror []string
	if spec.Offline {
		localMirror = []string{"--local-mirror=" + spec.LocalMirror}
		for _, dir := range spec.LocalRepositories {
			if err := m.createrepo(ctx, output, dir); err != nil {
				return fail("createrepo_c could not index "+dir, err)
			}
		}
	}
	if err := m.mkosi(ctx, output, slices.Concat([]string{
		"--directory=" + spec.ToolsConfigDir,
		"--package-manager-tree=" + filepath.Join(spec.ToolsConfigDir, fedoraPackageManagerTree),
		"--cache-dir=" + spec.CacheDir,
		"--output-dir=" + spec.ToolsOutputDir,
		"--finalize-script=" + filepath.Join(spec.ToolsConfigDir, fedoraFinalizeName),
	}, localMirror, []string{"--force", "build"})); err != nil {
		return fail("mkosi could not make the tools tree", err)
	}
	if err := os.Rename(filepath.Join(spec.ToolsOutputDir, fedoraToolsInstalledFile), spec.ToolsRecordPath); err != nil {
		return fail("the tools tree's build recorded no packages", err)
	}
	progress.Report(ProgressEvent{Phase: PhaseMakeToolsTree, Kind: EventPhaseFinished})
	// Offline there is nothing to download: the packages are local.
	if spec.Offline {
		parser.advance(PhaseInstallRequested)
	} else {
		parser.advance(PhaseDownload)
	}

	if err := m.mkosi(ctx, output, slices.Concat([]string{
		"--directory=" + spec.ImageConfigDir,
		"--tools-tree=" + filepath.Join(spec.ToolsOutputDir, mkosiToolsOutput),
		"--package-manager-tree=" + filepath.Join(spec.ImageConfigDir, fedoraPackageManagerTree),
		"--cache-dir=" + spec.CacheDir,
		"--output-dir=" + spec.ImageOutputDir,
		"--source-date-epoch=" + strconv.FormatInt(spec.SourceDateEpoch, 10),
		"--postinst-script=" + filepath.Join(spec.ImageConfigDir, fedoraProvisionName),
		"--finalize-script=" + filepath.Join(spec.ImageConfigDir, fedoraFinalizeName),
	}, localMirror, []string{"--force", "build"})); err != nil {
		return fail("mkosi could not make the image", err)
	}
	lines.flush()

	tarball, err := os.Create(spec.TarballPath)
	if err != nil {
		return err
	}
	runCommand := m.RunCommand
	if runCommand == nil {
		runCommand = runInterruptibly
	}
	gzipErr := runCommand(ctx, "gzip", []string{"-n", "-c", filepath.Join(spec.ImageOutputDir, mkosiImageOutput+".tar")}, nil, tarball, output)
	if closeErr := tarball.Close(); gzipErr == nil {
		gzipErr = closeErr
	}
	if gzipErr != nil {
		return fail("gzip could not compress the image", gzipErr)
	}
	parser.advance(phaseCount)
	return nil
}

// mkosiWrapper runs mkosi with umask 022, whatever the caller's: the modes
// dnf5 gives its state files follow the umask, and they are part of the
// image. It also unsets the two variables of the host's that mkosi reads
// and that would change what it builds with: MKOSI_DNF names the package
// manager, and MKOSI_INTERPRETER the Python of its helpers. The rest of
// what it hands its sandbox it sets itself.
const mkosiWrapper = `umask 022 && unset MKOSI_DNF MKOSI_INTERPRETER && exec mkosi "$@"`

// mkosi runs mkosi with args through mkosiWrapper.
func (m *Mkosi) mkosi(ctx context.Context, output io.Writer, args []string) error {
	runCommand := m.RunCommand
	if runCommand == nil {
		runCommand = runInterruptibly
	}
	return runCommand(ctx, "sh", append([]string{"-c", mkosiWrapper, "mkosi"}, args...), nil, output, output)
}

// createrepo gives a local repository its index with the host's
// createrepo_c, with umask 022 too: mkosi's sandbox reads the index as a
// subordinate uid.
func (m *Mkosi) createrepo(ctx context.Context, output io.Writer, dir string) error {
	runCommand := m.RunCommand
	if runCommand == nil {
		runCommand = runInterruptibly
	}
	return runCommand(ctx, "sh", []string{"-c", `umask 022 && exec createrepo_c "$@"`, "createrepo_c", "--quiet", "--", dir}, nil, output, output)
}

// mkosiProgress turns mkosi's output into phases. mkosi marks each of its
// steps with a line that starts with ‣; a phase starts at the step that
// begins it and finishes when a later phase starts. Phases only move
// forward: mkosi installs twice, filesystem alone first.
type mkosiProgress struct {
	progress Progress
	phases   []Phase // the build's, in order
	phase    Phase
	started  bool
}

// mkosiSteps are the step lines that begin a phase of the image build.
var mkosiSteps = []struct {
	marker string
	phase  Phase
}{
	{"Running transaction", PhaseInstallRequested},
	{"Running postinstall script", PhaseProvision},
	{"Creating tar archive", PhaseCreateTarball},
}

// line reports one line of output. The tools tree's build prints some of the
// same markers, so they count only once the image's build has begun.
func (p *mkosiProgress) line(line string) {
	if p.phase != PhaseMakeToolsTree {
		for _, step := range mkosiSteps {
			if strings.Contains(line, step.marker) {
				p.advance(step.phase)
			}
		}
	}
	p.progress.Report(ProgressEvent{Phase: p.phase, Kind: EventLogLine, Line: line})
}

// advance finishes the running phase and starts phase, unless phase is not
// later; phaseCount finishes the last one.
func (p *mkosiProgress) advance(phase Phase) {
	if !p.after(phase) {
		return
	}
	if p.started || p.phase != PhaseMakeToolsTree {
		p.progress.Report(ProgressEvent{Phase: p.phase, Kind: EventPhaseFinished})
	}
	p.phase, p.started = phase, true
	if phase != phaseCount {
		p.progress.Report(ProgressEvent{Phase: phase, Kind: EventPhaseStarted})
	}
}

// after reports whether phase comes after the running one in the build's
// phases.
func (p *mkosiProgress) after(phase Phase) bool {
	order := append(slices.Clone(p.phases), phaseCount)
	return slices.Index(order, phase) > slices.Index(order, p.phase)
}

// lineSplitter hands complete lines to emit as output arrives.
type lineSplitter struct {
	emit    func(line string)
	pending []byte
}

func (s *lineSplitter) Write(data []byte) (int, error) {
	s.pending = append(s.pending, data...)
	for {
		index := bytes.IndexByte(s.pending, '\n')
		if index < 0 {
			return len(data), nil
		}
		s.emit(strings.TrimRight(string(s.pending[:index]), "\r"))
		s.pending = s.pending[index+1:]
	}
}

// flush hands on a last line that ended without a newline.
func (s *lineSplitter) flush() {
	if len(s.pending) > 0 {
		s.emit(string(s.pending))
		s.pending = nil
	}
}
