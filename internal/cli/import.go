package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"frostroot/internal/paxstrip"
	"frostroot/internal/recipe"
	"frostroot/internal/wslapi"
)

const importUsageText = `usage: frostroot import NAME INSTALL-DIR TARBALL

Register TARBALL, a .tar.gz that frostroot build wrote, as the WSL
distribution NAME, with its file system in INSTALL-DIR: what
wsl --import NAME INSTALL-DIR TARBALL does, for a Windows without
wsl --import (Windows 10 before version 1903, or a machine with only WSL 1).
Run it there as the Windows binary, frostroot-windows-amd64.exe; it needs no
administrator. Never prompts.

INSTALL-DIR must not exist yet, or be empty, and the directory it goes in
must exist. import writes a copy of TARBALL beside INSTALL-DIR without the
pax records SCHILY.xattr.system.posix_acl_*, the ACLs of /var/log/journal,
which WSL 1 refuses; nothing else in it changes. Windows puts the file
system beside the program that registers it, so import runs a copy of
itself from INSTALL-DIR to register that copy, and removes both afterwards.
It then makes the user the image's /etc/wsl.conf names the default, which
this Windows does not read from the file itself; with none, root stays the
default. Start the distribution with: wsl -d NAME

A failed or interrupted import leaves nothing registered, and removes
INSTALL-DIR if it made it.

`

// windowsBinaryName is the Windows build of frostroot, which import is.
const windowsBinaryName = "frostroot-windows-amd64.exe"

// importSecondStageFlag starts import's second stage, which import runs
// from the copy of frostroot it places in the install directory. Only import
// itself runs it, so the usage text does not name it.
const importSecondStageFlag = "--from-install-dir"

// installedCopyName is the copy of frostroot in the install directory while
// import runs.
const installedCopyName = "frostroot-import.exe"

// windowsTempDirName is the directory Windows leaves in the install
// directory after a registration, refused or undone, empty.
const windowsTempDirName = "temp"

// strippedCopyPattern names the copy of the tarball import registers, beside
// the install directory.
const strippedCopyPattern = ".frostroot-import-*.tar.gz"

// launchTimeout bounds each command import runs in the new distribution.
// Each took a third of a second in the experiments, the first start of the
// distribution included.
const launchTimeout = 2 * time.Minute

// wslConfCommand is how import reads the image's own wsl.conf.
const wslConfCommand = "cat /etc/wsl.conf"

// rootUserName is the user a distribution starts as until it is configured.
const rootUserName = "root"

// uidPattern is what id -u prints: a decimal number of at most ten digits,
// which a uid_t holds.
var uidPattern = regexp.MustCompile(`^[0-9]{1,10}$`)

// wslAPI is what import asks of Windows: the functions of wslapi.dll.
type wslAPI interface {
	IsRegistered(name string) (bool, error)
	Register(name, tarballPath string) error
	Unregister(name string) error
	Configure(name string, defaultUID uint32, flags wslapi.DistributionFlags) error
	Launch(ctx context.Context, name, command string) (output []byte, exitCode uint32, err error)
}

// windowsWSL is wslAPI answered by wslapi.dll.
type windowsWSL struct{}

func (windowsWSL) IsRegistered(name string) (bool, error)  { return wslapi.IsRegistered(name) }
func (windowsWSL) Register(name, tarballPath string) error { return wslapi.Register(name, tarballPath) }
func (windowsWSL) Unregister(name string) error            { return wslapi.Unregister(name) }
func (windowsWSL) Configure(name string, defaultUID uint32, flags wslapi.DistributionFlags) error {
	return wslapi.Configure(name, defaultUID, flags)
}
func (windowsWSL) Launch(ctx context.Context, name, command string) ([]byte, uint32, error) {
	return wslapi.Launch(ctx, name, command)
}

// importJob is one import: what it was asked, and what it has made so far,
// which is what it removes again.
type importJob struct {
	name        string
	installDir  string
	tarballPath string
	// createInstallDir is true when the install directory did not exist,
	// and createdInstallDir once import made it.
	createInstallDir  bool
	createdInstallDir bool
	strippedPath      string // the copy of the tarball, once created
	installedCopyPath string // the copy of frostroot, once created
}

func (a *App) runImport(args []string) int {
	if len(args) > 0 && args[0] == importSecondStageFlag {
		return a.runImportSecondStage(args[1:])
	}
	flags := a.newFlagSet("import", importUsageText)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitSuccess
		}
		return exitUserError
	}
	if flags.NArg() != 3 {
		a.stderrf("frostroot import: want NAME, INSTALL-DIR and TARBALL, got %d arguments\n", flags.NArg())
		flags.Usage()
		return exitUserError
	}
	if a.GOOS != "windows" {
		a.stderrf("frostroot: import registers a WSL distribution, which only Windows does: run the Windows binary, %s, on the Windows machine\n", windowsBinaryName)
		return exitUserError
	}
	job, exitCode := a.checkImport(flags.Arg(0), flags.Arg(1), flags.Arg(2))
	if exitCode != exitSuccess {
		return exitCode
	}
	ctx, stopSignalHandling := a.notifyInterrupt()
	defer stopSignalHandling()
	exitCode = a.importFromInstalledCopy(ctx, &job)
	a.cleanUpImport(job, exitCode == exitSuccess)
	return exitCode
}

// checkImport checks everything it can before import touches anything,
// and returns the job, or the exit code to stop with.
func (a *App) checkImport(name, installDirArg, tarballArg string) (importJob, int) {
	if err := recipe.CheckImageName(name); err != nil {
		a.stderrf("frostroot import: NAME: %v\n", err)
		return importJob{}, exitUserError
	}
	tarballPath, err := filepath.Abs(tarballArg)
	if err != nil {
		a.stderrf("frostroot import: TARBALL: %v\n", err)
		return importJob{}, exitUserError
	}
	if !strings.HasSuffix(strings.ToLower(tarballPath), ".tar.gz") {
		a.stderrf("frostroot import: %s is not a .tar.gz: import takes the tarball frostroot build wrote\n", tarballPath)
		return importJob{}, exitUserError
	}
	tarballInfo, err := os.Stat(tarballPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		a.stderrf("frostroot import: %s does not exist\n", tarballPath)
		return importJob{}, exitUserError
	case err != nil:
		a.stderrf("frostroot import: TARBALL: %v\n", err)
		return importJob{}, exitUserError
	case !tarballInfo.Mode().IsRegular():
		a.stderrf("frostroot import: %s is not a file\n", tarballPath)
		return importJob{}, exitUserError
	}
	job := importJob{name: name, tarballPath: tarballPath}
	if !a.checkInstallDir(installDirArg, &job) {
		return importJob{}, exitUserError
	}
	registered, err := a.wsl.IsRegistered(name)
	if err != nil {
		a.stderrf("frostroot import: %v\n", err)
		return importJob{}, importExitCode(err)
	}
	if registered {
		a.stderrf("frostroot import: a distribution named %s is already registered; choose another name, or unregister it first with: wslconfig /unregister %s\n", name, name)
		return importJob{}, exitUserError
	}
	return job, exitSuccess
}

// checkInstallDir checks that the install directory is absent or empty, in
// a directory that exists, and fills it into job. Absent or empty, whatever
// is in it after a failure is this import's, so the import can remove it.
func (a *App) checkInstallDir(installDirArg string, job *importJob) bool {
	installDir, err := filepath.Abs(installDirArg)
	if err != nil {
		a.stderrf("frostroot import: INSTALL-DIR: %v\n", err)
		return false
	}
	parentDir := filepath.Dir(installDir)
	if parentDir == installDir {
		a.stderrf("frostroot import: INSTALL-DIR cannot be %s, the top of a drive\n", installDir)
		return false
	}
	if parentInfo, err := os.Stat(parentDir); err != nil || !parentInfo.IsDir() {
		a.stderrf("frostroot import: %s, the directory INSTALL-DIR goes in, does not exist\n", parentDir)
		return false
	}
	job.installDir = installDir
	installDirInfo, err := os.Lstat(installDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		job.createInstallDir = true
		return true
	case err != nil:
		a.stderrf("frostroot import: INSTALL-DIR: %v\n", err)
		return false
	case !installDirInfo.IsDir():
		a.stderrf("frostroot import: %s exists and is not a directory\n", installDir)
		return false
	}
	empty, err := isEmptyDirectory(installDir)
	if err != nil {
		a.stderrf("frostroot import: INSTALL-DIR: %v\n", err)
		return false
	}
	if !empty {
		a.stderrf("frostroot import: %s is not empty: import needs a new or empty directory, so that a failed import removes only what it made\n", installDir)
		return false
	}
	return true
}

// isEmptyDirectory reports whether the directory at path holds nothing,
// reading at most one name of it.
func isEmptyDirectory(path string) (bool, error) {
	directory, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = directory.Close() }() // read-only: closing cannot lose data
	if _, err := directory.Readdirnames(1); errors.Is(err, io.EOF) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	return false, nil
}

// importFromInstalledCopy makes the install directory, the copy of the
// tarball and the copy of frostroot, and runs the second stage from that
// copy. It returns the exit code; cleanUpImport removes what it made.
func (a *App) importFromInstalledCopy(ctx context.Context, job *importJob) int {
	if job.createInstallDir {
		if err := os.Mkdir(job.installDir, 0o755); err != nil {
			a.stderrf("frostroot import: %v\n", err)
			return exitUserError
		}
		job.createdInstallDir = true
	}
	if exitCode := a.writeStrippedCopy(ctx, job); exitCode != exitSuccess {
		return exitCode
	}
	if ctx.Err() != nil {
		return a.reportImportInterrupted()
	}
	executablePath, err := a.executable()
	if err != nil {
		a.stderrf("frostroot import: finding this program's own file: %v\n", err)
		return exitBuildFailed
	}
	installedCopyPath := filepath.Join(job.installDir, installedCopyName)
	if err := copyExecutable(executablePath, installedCopyPath, func() { job.installedCopyPath = installedCopyPath }); err != nil {
		a.stderrf("frostroot import: copying %s into %s: %v\n", executablePath, job.installDir, err)
		return exitBuildFailed
	}
	// The second stage sees Ctrl-C itself, on the same console, and must
	// finish undoing a registration, so it is waited for, not stopped.
	exitCode, err := a.runInstalledCopy(ctx, installedCopyPath, []string{"import", importSecondStageFlag, job.name, job.installDir, job.strippedPath})
	if err != nil {
		a.stderrf("frostroot import: running %s: %v\n", installedCopyPath, err)
		return exitBuildFailed
	}
	return exitCode
}

// writeStrippedCopy writes the copy of the tarball import registers, beside
// the install directory, without the records WSL 1 refuses.
func (a *App) writeStrippedCopy(ctx context.Context, job *importJob) int {
	a.stderrf("frostroot: copying %s without the pax records WSL 1 refuses (%s*)\n", job.tarballPath, paxstrip.DroppedKeyPrefix)
	source, err := os.Open(job.tarballPath)
	if err != nil {
		a.stderrf("frostroot import: %v\n", err)
		return exitUserError
	}
	defer func() { _ = source.Close() }() // read-only: closing cannot lose data
	stripped, err := os.CreateTemp(filepath.Dir(job.installDir), strippedCopyPattern)
	if err != nil {
		a.stderrf("frostroot import: %v\n", err)
		return exitUserError
	}
	job.strippedPath = stripped.Name()
	dropped, err := paxstrip.Copy(stripped, contextReader{ctx: ctx, reader: source})
	closeErr := stripped.Close()
	switch {
	case ctx.Err() != nil:
		return a.reportImportInterrupted()
	case errors.Is(err, paxstrip.ErrNotTarball):
		a.stderrf("frostroot import: %s: %v\n", job.tarballPath, err)
		return exitUserError
	case err != nil:
		a.stderrf("frostroot import: copying %s: %v\n", job.tarballPath, err)
		return exitBuildFailed
	case closeErr != nil:
		a.stderrf("frostroot import: writing %s: %v\n", job.strippedPath, closeErr)
		return exitBuildFailed
	}
	if dropped == 0 {
		a.stderrf("frostroot: the tarball holds no such record, so the copy is the same archive\n")
	} else {
		a.stderrf("frostroot: left out %d such records\n", dropped)
	}
	return exitSuccess
}

// contextReader stops reading once ctx ends, so that Ctrl-C stops the copy
// of a tarball of hundreds of megabytes.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

// copyExecutable copies the running program to destination, a new file,
// and calls created once the file exists, so that a copy that fails half
// way is removed too.
func copyExecutable(sourcePath, destination string, created func()) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }() // read-only: closing cannot lose data
	target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	created()
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close() // the copy failed already; that error is the one to report
		return err
	}
	return target.Close()
}

// startInstalledCopy runs the copy of frostroot in the install directory as
// a process of its own, sharing this one's console, and returns its exit
// code. It is not stopped when ctx ends: it sees Ctrl-C itself.
func (a *App) startInstalledCopy(_ context.Context, executablePath string, args []string) (int, error) {
	command := exec.Command(executablePath, args...)
	command.Stdin, command.Stdout, command.Stderr = a.Stdin, a.Stdout, a.Stderr
	err := command.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 0, err
	}
	return exitSuccess, nil
}

// cleanUpImport removes what import made, however it ended: the copy of the
// tarball and the copy of frostroot always, and after a failure what it can
// of the install directory. Only by names it knows and with os.Remove,
// which takes an empty directory only: Go code never deletes a root file
// system, so a rootfs Windows left keeps the directory, and the message
// says so.
func (a *App) cleanUpImport(job importJob, succeeded bool) {
	for _, path := range []string{job.strippedPath, job.installedCopyPath} {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			a.stderrf("warning: could not remove %s: %v\n", path, err)
		}
	}
	if succeeded || job.installDir == "" {
		return
	}
	// The directory was empty or absent before, so a temp directory in it
	// is the one Windows left.
	if err := os.Remove(filepath.Join(job.installDir, windowsTempDirName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.stderrf("warning: could not remove %s: %v\n", filepath.Join(job.installDir, windowsTempDirName), err)
	}
	if !job.createdInstallDir {
		return
	}
	if err := os.Remove(job.installDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.stderrf("frostroot import: %s is left in place, since it still holds files: %v\n", job.installDir, err)
	}
}

// runImportSecondStage is import's second stage, run from the copy of
// frostroot in the install directory, since Windows puts the file system
// beside the program that registers it: register the copy of the tarball,
// make the image's default user the distribution's, and unregister again
// if anything fails.
func (a *App) runImportSecondStage(args []string) int {
	if len(args) != 3 || a.GOOS != "windows" {
		a.stderrf("frostroot import: %s is import's own second stage, run from the copy of frostroot it places in the install directory\n", importSecondStageFlag)
		return exitUserError
	}
	name, installDir, tarballPath := args[0], args[1], args[2]
	if err := recipe.CheckImageName(name); err != nil {
		a.stderrf("frostroot import: NAME: %v\n", err)
		return exitUserError
	}
	executablePath, err := a.executable()
	if err != nil || !sameDirectory(filepath.Dir(executablePath), installDir) {
		a.stderrf("frostroot import: %s is import's own second stage, and runs only from the copy of frostroot it places in %s\n", importSecondStageFlag, installDir)
		return exitUserError
	}
	ctx, stopSignalHandling := a.notifyInterrupt()
	defer stopSignalHandling()

	a.stderrf("frostroot: registering %s in %s; Windows unpacks the tarball, which takes a minute or so\n", name, installDir)
	if err := a.wsl.Register(name, tarballPath); err != nil {
		a.stderrf("frostroot import: %v\n", err)
		return importExitCode(err)
	}
	// Register cannot be interrupted, so an interrupt while it ran is seen
	// only now.
	if ctx.Err() != nil {
		a.stderrf("frostroot: import interrupted\n")
		return a.undoRegistration(name, exitInterrupted)
	}
	userName, err := a.configureDefaultUser(ctx, name)
	if err != nil {
		if ctx.Err() != nil {
			a.stderrf("frostroot: import interrupted\n")
			return a.undoRegistration(name, exitInterrupted)
		}
		a.stderrf("frostroot import: %v\n", err)
		return a.undoRegistration(name, importExitCode(err))
	}
	a.stdoutf("Registered %s, with its file system in %s; it starts as %s.\nStart it with:\n  wsl -d %s\n", name, installDir, userName, name)
	return exitSuccess
}

// sameDirectory reports whether two paths name one directory, however each
// is spelled: Windows paths differ in case and in short names.
func sameDirectory(first, second string) bool {
	firstInfo, err := os.Stat(first)
	if err != nil {
		return false
	}
	secondInfo, err := os.Stat(second)
	if err != nil {
		return false
	}
	return os.SameFile(firstInfo, secondInfo)
}

// configureDefaultUser makes the user the distribution's /etc/wsl.conf names
// in [user] default the one it starts as, and returns that user's name.
// Before it, the distribution starts as root, which is what stays when the
// file names no one.
func (a *App) configureDefaultUser(ctx context.Context, name string) (string, error) {
	output, status, err := a.launchInDistribution(ctx, name, wslConfCommand)
	if err != nil {
		return "", fmt.Errorf("reading /etc/wsl.conf: %w", err)
	}
	if status != 0 {
		a.stderrf("warning: /etc/wsl.conf could not be read (cat exited %d), so the distribution starts as root\n", status)
		return rootUserName, nil
	}
	defaultUser, found := wslConfDefaultUser(output)
	if !found {
		a.stderrf("warning: /etc/wsl.conf names no default user in [user], so the distribution starts as root\n")
		return rootUserName, nil
	}
	if defaultUser == rootUserName {
		return rootUserName, nil
	}
	// The name came out of the image and goes into a command line. It is
	// not quoted: how WslLaunch splits that line is Windows' affair, and
	// the rule leaves nothing in it that a shell would read.
	if err := recipe.CheckAccountName(defaultUser); err != nil {
		return "", fmt.Errorf("/etc/wsl.conf names a default user frostroot does not pass to a command: %w", err)
	}
	output, status, err = a.launchInDistribution(ctx, name, "id -u "+defaultUser)
	if err != nil {
		return "", fmt.Errorf("asking the distribution for the uid of %s: %w", defaultUser, err)
	}
	if status != 0 {
		return "", fmt.Errorf("the distribution has no user %s, whom /etc/wsl.conf names as the default (id -u exited %d)", defaultUser, status)
	}
	uid, err := parseUID(output)
	if err != nil {
		return "", fmt.Errorf("the uid of %s: %w", defaultUser, err)
	}
	if err := a.wsl.Configure(name, uid, wslapi.DefaultFlags); err != nil {
		return "", fmt.Errorf("making %s the default user: %w", defaultUser, err)
	}
	return defaultUser, nil
}

// launchInDistribution runs command in the distribution, for at most
// launchTimeout.
func (a *App) launchInDistribution(ctx context.Context, name, command string) ([]byte, uint32, error) {
	launchCtx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()
	return a.wsl.Launch(launchCtx, name, command)
}

// parseUID reads what id -u printed: one decimal number and a newline.
func parseUID(output []byte) (uint32, error) {
	text := strings.TrimSuffix(string(output), "\n")
	if !uidPattern.MatchString(text) {
		return 0, fmt.Errorf("id -u printed %q, not a number", output)
	}
	uid, err := strconv.ParseUint(text, 10, 32)
	// 4294967295 is (uid_t)-1, which no user has.
	if err != nil || uid == 1<<32-1 {
		return 0, fmt.Errorf("id -u printed %q, which is no uid", output)
	}
	return uint32(uid), nil
}

// wslConfDefaultUser returns the default key of the [user] section of a
// wsl.conf, and whether there is one: section and key names in any case,
// spaces around the "=", "#" and ";" comment lines, CRLF line ends, and a
// value in double quotes. The last one counts, and an empty one names no
// one. The file came out of an image, so what this returns is checked
// before it is used.
func wslConfDefaultUser(content []byte) (string, bool) {
	section := ""
	defaultUser, found := "", false
	for line := range strings.SplitSeq(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			section = ""
			if end := strings.IndexByte(line, ']'); end > 0 {
				section = strings.TrimSpace(line[1:end])
			}
			continue
		}
		key, value, hasValue := strings.Cut(line, "=")
		if !hasValue || !strings.EqualFold(section, "user") || !strings.EqualFold(strings.TrimSpace(key), "default") {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		defaultUser, found = value, value != ""
	}
	return defaultUser, found
}

// undoRegistration unregisters the distribution after a failure, so that a
// failed import leaves nothing registered, and returns exitCode.
func (a *App) undoRegistration(name string, exitCode int) int {
	a.stderrf("frostroot: unregistering %s\n", name)
	if err := a.wsl.Unregister(name); err != nil {
		a.stderrf("frostroot import: could not unregister %s: %v; remove it with: wslconfig /unregister %s\n", name, err, name)
	}
	return exitCode
}

// reportImportInterrupted says that import stopped at Ctrl-C before it
// registered anything, and returns the exit code for that.
func (a *App) reportImportInterrupted() int {
	a.stderrf("frostroot: import interrupted; nothing was registered\n")
	return exitInterrupted
}

// importExitCode is the exit code of a failure of the WSL API: 1 for what
// the user can fix, a Windows without WSL or a name already taken, and 2
// for the rest, a tarball Windows refused among them.
func importExitCode(err error) int {
	switch {
	case errors.Is(err, wslapi.ErrNoWSL), errors.Is(err, wslapi.ErrNotWindows), errors.Is(err, wslapi.ErrAlreadyRegistered):
		return exitUserError
	default:
		return exitBuildFailed
	}
}
