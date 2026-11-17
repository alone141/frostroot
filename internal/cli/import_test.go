package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/recipe"
	"frostroot/internal/wslapi"
)

// fakeLaunch is what a command run in the fake distribution answers.
type fakeLaunch struct {
	output   string
	exitCode uint32
	err      error
}

// fakeWSL implements wslAPI as the experiments found Windows does:
// Register unpacks into rootfs beside the program that calls it, here the
// copy of frostroot in the install directory, and leaves an empty temp
// beside it, also when it refuses the tarball; Unregister removes rootfs
// and leaves the rest.
type fakeWSL struct {
	t          *testing.T
	installDir string
	registered map[string]bool
	calls      []string
	launches   map[string]fakeLaunch
	// registerErr, unregisterErr and configureErr fail those calls.
	registerErr, unregisterErr, configureErr error
	// duringRegister runs inside Register, as a Ctrl-C would arrive.
	duringRegister func()
	// registeredTarball is the tarball Register was given, decompressed,
	// and registeredPath its path.
	registeredTarball []byte
	registeredPath    string
}

func (f *fakeWSL) IsRegistered(name string) (bool, error) {
	f.calls = append(f.calls, "IsRegistered "+name)
	return f.registered[name], nil
}

func (f *fakeWSL) Register(name, tarballPath string) error {
	f.calls = append(f.calls, "Register "+name)
	f.registeredPath = tarballPath
	if _, err := os.Stat(filepath.Join(f.installDir, installedCopyName)); err != nil {
		f.t.Errorf("Register ran without the copy of frostroot in the install directory: %v", err)
	}
	compressed, err := os.ReadFile(tarballPath)
	if err != nil {
		f.t.Errorf("Register got a tarball it cannot read: %v", err)
	} else if reader, err := gzip.NewReader(bytes.NewReader(compressed)); err != nil {
		f.t.Errorf("Register got a tarball that is not gzip: %v", err)
	} else if f.registeredTarball, err = io.ReadAll(reader); err != nil {
		f.t.Errorf("Register got a tarball that does not decompress: %v", err)
	}
	if f.duringRegister != nil {
		f.duringRegister()
	}
	if err := os.MkdirAll(filepath.Join(f.installDir, windowsTempDirName), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if f.registerErr != nil {
		return f.registerErr
	}
	if err := os.MkdirAll(filepath.Join(f.installDir, "rootfs", "etc"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.registered[name] = true
	return nil
}

func (f *fakeWSL) Unregister(name string) error {
	f.calls = append(f.calls, "Unregister "+name)
	if f.unregisterErr != nil {
		return f.unregisterErr
	}
	if err := os.RemoveAll(filepath.Join(f.installDir, "rootfs")); err != nil {
		f.t.Fatal(err)
	}
	delete(f.registered, name)
	return nil
}

func (f *fakeWSL) Configure(name string, defaultUID uint32, flags wslapi.DistributionFlags) error {
	f.calls = append(f.calls, fmt.Sprintf("Configure %s %d %d", name, defaultUID, flags))
	return f.configureErr
}

func (f *fakeWSL) Launch(_ context.Context, name, command string) ([]byte, uint32, error) {
	f.calls = append(f.calls, "Launch "+name+": "+command)
	answer, known := f.launches[command]
	if !known {
		return []byte("command not found\n"), 127, nil
	}
	return []byte(answer.output), answer.exitCode, answer.err
}

// launched returns the commands Launch ran, in order.
func (f *fakeWSL) launched() []string {
	var commands []string
	for _, call := range f.calls {
		if command, isLaunch := strings.CutPrefix(call, "Launch lab: "); isLaunch {
			commands = append(commands, command)
		}
	}
	return commands
}

// frostrootWSLConf is the wsl.conf frostroot writes into an image.
const frostrootWSLConf = "[boot]\nsystemd=true\n\n[user]\ndefault=student\n\n[time]\nuseWindowsTimezone=false\n"

// importFixture is an import about to run: a tarball with both ACL records
// and a capability, an install directory that does not exist yet, the
// running program's file, and a fake WSL whose distribution names student
// as its default user.
type importFixture struct {
	t              *testing.T
	app            *App
	wsl            *fakeWSL
	stdout, stderr *bytes.Buffer
	baseDir        string
	installDir     string
	tarballPath    string
	executablePath string
	// interrupt is a Ctrl-C, which both stages see.
	interrupt context.CancelFunc
}

func newImportFixture(t *testing.T) *importFixture {
	t.Helper()
	baseDir := t.TempDir()
	fixture := &importFixture{
		t:              t,
		stdout:         &bytes.Buffer{},
		stderr:         &bytes.Buffer{},
		baseDir:        baseDir,
		installDir:     filepath.Join(baseDir, "lab"),
		tarballPath:    filepath.Join(baseDir, "lab-ubuntu-24.04-amd64.tar.gz"),
		executablePath: filepath.Join(baseDir, "bin", "frostroot-windows-amd64.exe"),
	}
	fixture.wsl = &fakeWSL{t: t, installDir: fixture.installDir, registered: map[string]bool{"Ubuntu": true}, launches: map[string]fakeLaunch{
		wslConfCommand:  {output: frostrootWSLConf},
		"id -u student": {output: "1000\n"},
	}}
	if err := os.WriteFile(fixture.tarballPath, importTarball(t, true), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(fixture.executablePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.executablePath, []byte("MZ this program"), 0o755); err != nil {
		t.Fatal(err)
	}
	interrupted, interrupt := context.WithCancel(context.Background())
	t.Cleanup(interrupt)
	fixture.interrupt = interrupt
	notifyInterrupt := func() (context.Context, context.CancelFunc) { return interrupted, func() {} }
	fixture.app = &App{
		Stdout: fixture.stdout, Stderr: fixture.stderr, RecipeDir: baseDir, GOOS: "windows",
		wsl:              fixture.wsl,
		executable:       func() (string, error) { return fixture.executablePath, nil },
		interruptContext: notifyInterrupt,
	}
	// The second stage runs here, as the copy in the install directory
	// would run it.
	fixture.app.runInstalledCopy = func(_ context.Context, executablePath string, args []string) (int, error) {
		installedCopy := &App{
			Stdout: fixture.stdout, Stderr: fixture.stderr, RecipeDir: baseDir, GOOS: "windows",
			wsl:              fixture.wsl,
			executable:       func() (string, error) { return executablePath, nil },
			interruptContext: notifyInterrupt,
		}
		return installedCopy.Run(args), nil
	}
	return fixture
}

func (f *importFixture) run(args ...string) int {
	return f.app.Run(append([]string{"import"}, args...))
}

func (f *importFixture) runDefault() int {
	return f.run("lab", f.installDir, f.tarballPath)
}

// importTarball is a small tarball as frostroot builds one: the journal's
// directory with both ACLs, when withACLs, and ping with its capability.
func importTarball(t *testing.T, withACLs bool) []byte {
	t.Helper()
	journalRecords := map[string]string{}
	if withACLs {
		journalRecords["SCHILY.xattr.system.posix_acl_access"] = "\x02\x00\x00\x00"
		journalRecords["SCHILY.xattr.system.posix_acl_default"] = "\x02\x00\x00\x00"
	}
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	writer := tar.NewWriter(compressed)
	for _, header := range []tar.Header{
		{Typeflag: tar.TypeDir, Name: "./var/log/journal/", Mode: 0o2755, PAXRecords: journalRecords, Format: tar.FormatPAX},
		{Typeflag: tar.TypeReg, Name: "./usr/bin/ping", Mode: 0o755, Size: 4, PAXRecords: map[string]string{"SCHILY.xattr.security.capability": "\x01\x00\x00\x02"}, Format: tar.FormatPAX},
	} {
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(make([]byte, header.Size)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

// assertNoCopiesLeft checks that neither the copy of the tarball nor
// the copy of frostroot is left.
func (f *importFixture) assertNoCopiesLeft(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(f.baseDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".frostroot-import-") {
			t.Errorf("the copy of the tarball is left: %s", entry.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(f.installDir, installedCopyName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy of frostroot is left in the install directory: %v", err)
	}
}

func (f *importFixture) assertNoInstallDir(t *testing.T) {
	t.Helper()
	if _, err := os.Lstat(f.installDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the install directory is left: %v", err)
	}
}

func TestImport(t *testing.T) {
	fixture := newImportFixture(t)
	if exitCode := fixture.runDefault(); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr:\n%s", exitCode, fixture.stderr)
	}
	wantCalls := []string{
		"IsRegistered lab",
		"Register lab",
		"Launch lab: cat /etc/wsl.conf",
		"Launch lab: id -u student",
		"Configure lab 1000 7",
	}
	if !slices.Equal(fixture.wsl.calls, wantCalls) {
		t.Errorf("calls = %q, want %q", fixture.wsl.calls, wantCalls)
	}
	if got := filepath.Dir(fixture.wsl.registeredPath); got != fixture.baseDir {
		t.Errorf("the registered copy is in %s, want beside the install directory, in %s", got, fixture.baseDir)
	}
	if bytes.Contains(fixture.wsl.registeredTarball, []byte("posix_acl")) {
		t.Error("the registered tarball still holds the ACL records")
	}
	if !bytes.Contains(fixture.wsl.registeredTarball, []byte("SCHILY.xattr.security.capability")) {
		t.Error("the registered tarball lost ping's capability")
	}
	for _, want := range []string{"Registered lab, with its file system in " + fixture.installDir + "; it starts as student.", "wsl -d lab\n"} {
		if !strings.Contains(fixture.stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, fixture.stdout)
		}
	}
	for _, want := range []string{"SCHILY.xattr.system.posix_acl_*", "left out 2 such records", "registering lab in " + fixture.installDir} {
		if !strings.Contains(fixture.stderr.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, fixture.stderr)
		}
	}
	fixture.assertNoCopiesLeft(t)
	if _, err := os.Stat(filepath.Join(fixture.installDir, "rootfs")); err != nil {
		t.Errorf("the distribution's file system is gone: %v", err)
	}
}

// TestImportIntoAnEmptyDirectory: an empty install directory is as good as
// none, and after a failure it stays, empty, since import did not make it.
func TestImportIntoAnEmptyDirectory(t *testing.T) {
	fixture := newImportFixture(t)
	if err := os.Mkdir(fixture.installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture.wsl.registerErr = &wslapi.HRESULTError{Function: "WslRegisterDistribution", Code: 0x80004005}
	if exitCode := fixture.runDefault(); exitCode != exitBuildFailed {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", exitCode, exitBuildFailed, fixture.stderr)
	}
	entries, err := os.ReadDir(fixture.installDir)
	if err != nil || len(entries) != 0 {
		t.Errorf("the install directory import did not make should be left empty: %v, %v", entries, err)
	}
}

func TestImportRefusesBeforeTouchingAnything(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		prepare   func(f *importFixture) []string
		wantInErr string
	}{
		{"too few arguments", func(f *importFixture) []string { return []string{"lab", f.installDir} }, "want NAME, INSTALL-DIR and TARBALL"},
		{"a name WSL cannot have", func(f *importFixture) []string { return []string{"my lab", f.installDir, f.tarballPath} }, "invalid image name"},
		{"no tarball", func(f *importFixture) []string {
			return []string{"lab", f.installDir, filepath.Join(f.baseDir, "missing.tar.gz")}
		}, "missing.tar.gz does not exist"},
		{"a tarball not named .tar.gz", func(f *importFixture) []string {
			renamed := filepath.Join(f.baseDir, "lab.tar")
			if err := os.Rename(f.tarballPath, renamed); err != nil {
				f.t.Fatal(err)
			}
			return []string{"lab", f.installDir, renamed}
		}, "is not a .tar.gz"},
		{"a directory for a tarball", func(f *importFixture) []string {
			directory := filepath.Join(f.baseDir, "dir.tar.gz")
			if err := os.Mkdir(directory, 0o755); err != nil {
				f.t.Fatal(err)
			}
			return []string{"lab", f.installDir, directory}
		}, "is not a file"},
		{"an install directory that is not empty", func(f *importFixture) []string {
			if err := os.MkdirAll(filepath.Join(f.installDir, "rootfs"), 0o755); err != nil {
				f.t.Fatal(err)
			}
			return []string{"lab", f.installDir, f.tarballPath}
		}, "is not empty"},
		{"an install directory that is a file", func(f *importFixture) []string {
			if err := os.WriteFile(f.installDir, nil, 0o644); err != nil {
				f.t.Fatal(err)
			}
			return []string{"lab", f.installDir, f.tarballPath}
		}, "is not a directory"},
		{"an install directory in no directory", func(f *importFixture) []string {
			return []string{"lab", filepath.Join(f.baseDir, "missing", "lab"), f.tarballPath}
		}, "does not exist"},
		{"a name already registered", func(f *importFixture) []string {
			return []string{"Ubuntu", f.installDir, f.tarballPath}
		}, "a distribution named Ubuntu is already registered"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newImportFixture(t)
			args := testCase.prepare(fixture)
			before := directoryListing(t, fixture.baseDir)
			if exitCode := fixture.run(args...); exitCode != exitUserError {
				t.Fatalf("exit code = %d, want %d; stderr:\n%s", exitCode, exitUserError, fixture.stderr)
			}
			if !strings.Contains(fixture.stderr.String(), testCase.wantInErr) {
				t.Errorf("stderr lacks %q:\n%s", testCase.wantInErr, fixture.stderr)
			}
			if after := directoryListing(t, fixture.baseDir); !slices.Equal(after, before) {
				t.Errorf("a refused import changed the directory:\nbefore %q\n after %q", before, after)
			}
			for _, call := range fixture.wsl.calls {
				if !strings.HasPrefix(call, "IsRegistered ") {
					t.Errorf("a refused import called %s", call)
				}
			}
		})
	}
}

// directoryListing lists everything under root, for comparing before and
// after.
func directoryListing(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
		paths = append(paths, path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// TestImportRefusesWhatIsNotATarball: the copy finds it, after the install
// directory is made, and everything made goes again, exit 1.
func TestImportRefusesWhatIsNotATarball(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		content func(t *testing.T) []byte
	}{
		{"not gzip", func(*testing.T) []byte { return []byte("this is not a tarball\n") }},
		{"cut short", func(t *testing.T) []byte {
			t.Helper()
			var raw bytes.Buffer
			reader, err := gzip.NewReader(bytes.NewReader(importTarball(t, true)))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(&raw, reader); err != nil {
				t.Fatal(err)
			}
			var cut bytes.Buffer
			writer := gzip.NewWriter(&cut)
			if _, err := writer.Write(raw.Bytes()[:700]); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			return cut.Bytes()
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newImportFixture(t)
			if err := os.WriteFile(fixture.tarballPath, testCase.content(t), 0o644); err != nil {
				t.Fatal(err)
			}
			if exitCode := fixture.runDefault(); exitCode != exitUserError {
				t.Fatalf("exit code = %d, want %d; stderr:\n%s", exitCode, exitUserError, fixture.stderr)
			}
			if !strings.Contains(fixture.stderr.String(), "not a gzip-compressed tar archive") {
				t.Errorf("stderr should say it is not a tarball:\n%s", fixture.stderr)
			}
			if slices.Contains(fixture.wsl.calls, "Register lab") {
				t.Error("what is not a tarball must not reach Register")
			}
			fixture.assertNoCopiesLeft(t)
			fixture.assertNoInstallDir(t)
		})
	}
}

func TestImportReportsARefusedRegistration(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		registerErr  error
		wantExitCode int
		wantInErr    string
	}{
		{"the tarball refused", &wslapi.HRESULTError{Function: "WslRegisterDistribution", Code: 0x80004005}, exitBuildFailed, "Windows could not unpack it (0x80004005)"},
		{"the name taken meanwhile", &wslapi.HRESULTError{Function: "WslRegisterDistribution", Code: 0x800700B7}, exitUserError, "already registered"},
		{"no WSL", fmt.Errorf("%w: loading wslapi.dll", wslapi.ErrNoWSL), exitUserError, "Windows Subsystem for Linux"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newImportFixture(t)
			fixture.wsl.registerErr = testCase.registerErr
			if exitCode := fixture.runDefault(); exitCode != testCase.wantExitCode {
				t.Fatalf("exit code = %d, want %d; stderr:\n%s", exitCode, testCase.wantExitCode, fixture.stderr)
			}
			if !strings.Contains(fixture.stderr.String(), testCase.wantInErr) {
				t.Errorf("stderr lacks %q:\n%s", testCase.wantInErr, fixture.stderr)
			}
			if slices.Contains(fixture.wsl.calls, "Unregister lab") {
				t.Error("nothing was registered, so nothing is to be unregistered")
			}
			fixture.assertNoCopiesLeft(t)
			fixture.assertNoInstallDir(t)
		})
	}
}

// TestImportUnregistersAfterAFailure: a failed import leaves nothing
// registered, and nothing of the install directory it made.
func TestImportUnregistersAfterAFailure(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		change    func(f *fakeWSL)
		wantInErr string
	}{
		{"wsl.conf unreadable", func(f *fakeWSL) {
			f.launches[wslConfCommand] = fakeLaunch{err: &wslapi.HRESULTError{Function: "WslLaunch", Code: 0x80004005}}
		}, "reading /etc/wsl.conf"},
		{"id -u failing", func(f *fakeWSL) {
			f.launches["id -u student"] = fakeLaunch{output: "id: 'student': no such user\n", exitCode: 1}
		}, "has no user student"},
		{"id -u printing no number", func(f *fakeWSL) { f.launches["id -u student"] = fakeLaunch{output: "1000; reboot\n"} }, "not a number"},
		{"id -u printing (uid_t)-1", func(f *fakeWSL) { f.launches["id -u student"] = fakeLaunch{output: "4294967295\n"} }, "which is no uid"},
		{"id -u failing to start", func(f *fakeWSL) { f.launches["id -u student"] = fakeLaunch{err: context.DeadlineExceeded} }, "asking the distribution for the uid"},
		{"Configure failing", func(f *fakeWSL) {
			f.configureErr = &wslapi.HRESULTError{Function: "WslConfigureDistribution", Code: 0x80070005}
		}, "making student the default user"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newImportFixture(t)
			testCase.change(fixture.wsl)
			if exitCode := fixture.runDefault(); exitCode != exitBuildFailed {
				t.Fatalf("exit code = %d, want %d; stderr:\n%s", exitCode, exitBuildFailed, fixture.stderr)
			}
			if !strings.Contains(fixture.stderr.String(), testCase.wantInErr) {
				t.Errorf("stderr lacks %q:\n%s", testCase.wantInErr, fixture.stderr)
			}
			if got := fixture.wsl.calls[len(fixture.wsl.calls)-1]; got != "Unregister lab" {
				t.Errorf("the last call = %q, want Unregister lab; calls %q", got, fixture.wsl.calls)
			}
			if fixture.wsl.registered["lab"] {
				t.Error("lab is still registered")
			}
			fixture.assertNoCopiesLeft(t)
			fixture.assertNoInstallDir(t)
			if strings.Contains(fixture.stdout.String(), "Registered") {
				t.Errorf("a failed import reported success:\n%s", fixture.stdout)
			}
		})
	}
}

// TestImportKeepsAnInstallDirectoryWindowsDidNotEmpty: when unregistering
// fails, rootfs is still there, and import, which deletes no file system,
// leaves the directory and says how to remove the distribution.
func TestImportKeepsAnInstallDirectoryWindowsDidNotEmpty(t *testing.T) {
	fixture := newImportFixture(t)
	fixture.wsl.configureErr = &wslapi.HRESULTError{Function: "WslConfigureDistribution", Code: 0x80004005}
	fixture.wsl.unregisterErr = &wslapi.HRESULTError{Function: "WslUnregisterDistribution", Code: 0x80004005}
	if exitCode := fixture.runDefault(); exitCode != exitBuildFailed {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", exitCode, exitBuildFailed, fixture.stderr)
	}
	for _, want := range []string{"could not unregister lab", "wslconfig /unregister lab", fixture.installDir + " is left in place"} {
		if !strings.Contains(fixture.stderr.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, fixture.stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.installDir, "rootfs", "etc")); err != nil {
		t.Errorf("import removed the file system Windows left: %v", err)
	}
	fixture.assertNoCopiesLeft(t)
}

// TestImportHoldsTheDefaultUserToTheUserNameRule: the name comes out of the
// image and goes into a command line, so one that is not a user name never
// reaches Launch, and the import fails and unregisters.
func TestImportHoldsTheDefaultUserToTheUserNameRule(t *testing.T) {
	for _, wslConf := range []string{
		"[user]\ndefault=o'neil\n",
		"[user]\ndefault=two words\n",
		"[user]\ndefault=$(reboot)\n",
		"[user]\ndefault=`reboot`\n",
		"[user]\ndefault=student;reboot\n",
		"[user]\ndefault=\"student\nreboot\"\n",
		"[user]\ndefault=-u\n",
		"[user]\ndefault=Student\n",
		"[user]\ndefault=student\x00\n",
	} {
		t.Run(fmt.Sprintf("%q", wslConf), func(t *testing.T) {
			fixture := newImportFixture(t)
			fixture.wsl.launches[wslConfCommand] = fakeLaunch{output: wslConf}
			if exitCode := fixture.runDefault(); exitCode != exitBuildFailed {
				t.Fatalf("exit code = %d, want %d; stderr:\n%s", exitCode, exitBuildFailed, fixture.stderr)
			}
			if launched := fixture.wsl.launched(); !slices.Equal(launched, []string{wslConfCommand}) {
				t.Errorf("Launch ran %q; only %q may run before the name is checked", launched, wslConfCommand)
			}
			if !strings.Contains(fixture.stderr.String(), "invalid user name") {
				t.Errorf("stderr should say why:\n%s", fixture.stderr)
			}
			if !slices.Contains(fixture.wsl.calls, "Unregister lab") {
				t.Errorf("lab was not unregistered: %q", fixture.wsl.calls)
			}
		})
	}
}

// TestImportLeavesRootTheDefault: without a default user in wsl.conf the
// distribution starts as root, as it would anyway, and import says so.
func TestImportLeavesRootTheDefault(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		launch    fakeLaunch
		wantInErr string
	}{
		{"no wsl.conf", fakeLaunch{output: "cat: /etc/wsl.conf: No such file or directory\n", exitCode: 1}, "could not be read (cat exited 1)"},
		{"no [user] section", fakeLaunch{output: "[boot]\nsystemd=true\n"}, "names no default user"},
		{"default in another section", fakeLaunch{output: "[boot]\ndefault=student\n[user]\n"}, "names no default user"},
		{"an empty default", fakeLaunch{output: "[user]\ndefault=\n"}, "names no default user"},
		{"root", fakeLaunch{output: "[user]\ndefault = root\n"}, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newImportFixture(t)
			fixture.wsl.launches[wslConfCommand] = testCase.launch
			if exitCode := fixture.runDefault(); exitCode != exitSuccess {
				t.Fatalf("exit code = %d, stderr:\n%s", exitCode, fixture.stderr)
			}
			if !strings.Contains(fixture.stderr.String(), testCase.wantInErr) {
				t.Errorf("stderr lacks %q:\n%s", testCase.wantInErr, fixture.stderr)
			}
			if !strings.Contains(fixture.stdout.String(), "it starts as root.") {
				t.Errorf("stdout should name root as the default user:\n%s", fixture.stdout)
			}
			for _, call := range fixture.wsl.calls {
				if strings.HasPrefix(call, "Configure ") || strings.HasPrefix(call, "Unregister ") || strings.Contains(call, "id -u") {
					t.Errorf("unexpected call %s", call)
				}
			}
			fixture.assertNoCopiesLeft(t)
		})
	}
}

func TestImportInterrupted(t *testing.T) {
	t.Run("while Windows registers", func(t *testing.T) {
		fixture := newImportFixture(t)
		fixture.wsl.duringRegister = fixture.interrupt
		if exitCode := fixture.runDefault(); exitCode != exitInterrupted {
			t.Fatalf("exit code = %d, want %d; stderr:\n%s", exitCode, exitInterrupted, fixture.stderr)
		}
		if got := fixture.wsl.calls[len(fixture.wsl.calls)-1]; got != "Unregister lab" {
			t.Errorf("the last call = %q, want Unregister lab", got)
		}
		fixture.assertNoCopiesLeft(t)
		fixture.assertNoInstallDir(t)
	})
	t.Run("before", func(t *testing.T) {
		fixture := newImportFixture(t)
		fixture.interrupt()
		if exitCode := fixture.runDefault(); exitCode != exitInterrupted {
			t.Fatalf("exit code = %d, want %d; stderr:\n%s", exitCode, exitInterrupted, fixture.stderr)
		}
		if !strings.Contains(fixture.stderr.String(), "nothing was registered") {
			t.Errorf("stderr should say nothing was registered:\n%s", fixture.stderr)
		}
		if slices.Contains(fixture.wsl.calls, "Register lab") {
			t.Error("an import interrupted while copying must not register")
		}
		fixture.assertNoCopiesLeft(t)
		fixture.assertNoInstallDir(t)
	})
}

func TestImportRefusesOffWindows(t *testing.T) {
	fixture := newImportFixture(t)
	fixture.app.GOOS = "linux"
	before := directoryListing(t, fixture.baseDir)
	if exitCode := fixture.runDefault(); exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
	if !strings.Contains(fixture.stderr.String(), "run the Windows binary, frostroot-windows-amd64.exe, on the Windows machine") {
		t.Errorf("stderr should name the Windows binary:\n%s", fixture.stderr)
	}
	if len(fixture.wsl.calls) != 0 || !slices.Equal(directoryListing(t, fixture.baseDir), before) {
		t.Errorf("off Windows, import must touch nothing: calls %q", fixture.wsl.calls)
	}
}

// TestImportSecondStageRunsOnlyFromTheInstallDirectory: the second stage is
// where Windows puts the file system, so it refuses to run from anywhere
// else.
func TestImportSecondStageRunsOnlyFromTheInstallDirectory(t *testing.T) {
	fixture := newImportFixture(t)
	if err := os.Mkdir(fixture.installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if exitCode := fixture.run(importSecondStageFlag, "lab", fixture.installDir, fixture.tarballPath); exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
	if !strings.Contains(fixture.stderr.String(), "runs only from the copy of frostroot") {
		t.Errorf("stderr should say where the stage runs from:\n%s", fixture.stderr)
	}
	if len(fixture.wsl.calls) != 0 {
		t.Errorf("calls = %q, want none", fixture.wsl.calls)
	}
}

func TestImportUsage(t *testing.T) {
	fixture := newImportFixture(t)
	if exitCode := fixture.run("-h"); exitCode != exitSuccess {
		t.Fatalf("exit code = %d", exitCode)
	}
	usage := fixture.stderr.String()
	for _, want := range []string{"usage: frostroot import NAME INSTALL-DIR TARBALL", "wsl --import", "frostroot-windows-amd64.exe", "SCHILY.xattr.system.posix_acl_*", "/etc/wsl.conf", "wsl -d NAME", "leaves nothing registered"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage lacks %q:\n%s", want, usage)
		}
	}
	if strings.Contains(usage, importSecondStageFlag) {
		t.Errorf("usage names the second stage, which only import runs:\n%s", usage)
	}
}

func TestWSLConfDefaultUser(t *testing.T) {
	for _, testCase := range []struct {
		content   string
		wantUser  string
		wantFound bool
	}{
		{frostrootWSLConf, "student", true},
		{"[user]\r\ndefault=student\r\n", "student", true},
		{"  [ User ]  \n  Default  =  student  \n", "student", true},
		{"# [user]\n[user]\n; default=other\ndefault=\"student\"\n", "student", true},
		{"[user]\ndefault=first\ndefault=second\n", "second", true},
		{"[boot]\ndefault=student\n", "", false},
		{"default=student\n", "", false},
		{"[user\ndefault=student\n", "", false},
		{"[user]\ndefault=\n", "", false},
		{"[user]\ndefaults=student\n", "", false},
		{"", "", false},
	} {
		user, found := wslConfDefaultUser([]byte(testCase.content))
		if user != testCase.wantUser || found != testCase.wantFound {
			t.Errorf("wslConfDefaultUser(%q) = %q, %v, want %q, %v", testCase.content, user, found, testCase.wantUser, testCase.wantFound)
		}
	}
}

// FuzzWSLConfDefaultUser: the file comes out of an image. Reading it never
// panics, what it finds is a name on one line, and a name it finds that
// passes the user-name rule is one id -u can be given as it is.
func FuzzWSLConfDefaultUser(f *testing.F) {
	f.Add([]byte(frostrootWSLConf))
	f.Add([]byte("[user]\r\ndefault = \"o'neil\"\r\n[boot]\ndefault=root\n"))
	f.Add([]byte("[user]\ndefault=$(reboot)\n[user]\ndefault=\"a\nb\"\n"))
	f.Fuzz(func(t *testing.T, content []byte) {
		user, found := wslConfDefaultUser(content)
		if found == (user == "") {
			t.Fatalf("wslConfDefaultUser = %q, %v: found must mean a name", user, found)
		}
		if strings.Contains(user, "\n") {
			t.Fatalf("wslConfDefaultUser = %q, more than one line", user)
		}
		if found && recipe.CheckAccountName(user) == nil && strings.ContainsAny(user, " \t\r'\"$`;&|<>()\\*?[]{}~!#\x00") {
			t.Fatalf("%q passed the user-name rule with a character a shell reads", user)
		}
	})
}
