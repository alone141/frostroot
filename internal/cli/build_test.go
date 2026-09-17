package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frostroot/internal/builder"
	"frostroot/internal/deb"
	"frostroot/internal/deb/debtest"
	"frostroot/internal/recipe"
)

// fakeDpkgStatus is the dpkg status file fakeBootstrapper "downloads".
const fakeDpkgStatus = "Package: git\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1:1\n"

// fakeAptIndex is the Packages index fakeBootstrapper leaves in the copied
// apt lists, so that the lock gets a checksum for git.
const fakeAptIndex = "Package: git\nArchitecture: amd64\nVersion: 1:1\nFilename: pool/main/g/git/git_1%3a1_amd64.deb\nSize: 3\nSHA256: 2a8f4e0a1c3c4b0f37dc8b4e0d5a1c23f3c1b6d2a7a5e6e7f8091a2b3c4d5e6f\n"

// fakeBootstrapper writes the files a real bootstrap produces, without
// running mmdebstrap.
type fakeBootstrapper struct {
	runErr       error
	preflightErr error
	dpkgStatus   string                // defaults to fakeDpkgStatus
	lastSpec     builder.BootstrapSpec // zero until Run is called
}

func (f *fakeBootstrapper) Preflight(builder.BootstrapSpec) error {
	return f.preflightErr
}

func (f *fakeBootstrapper) Run(_ context.Context, spec builder.BootstrapSpec) error {
	f.lastSpec = spec
	if f.runErr != nil {
		return f.runErr
	}
	// Report one measured phase the way the real bootstrapper would.
	spec.Progress.Report(builder.ProgressEvent{Phase: builder.PhaseDownload, Kind: builder.EventPhaseStarted})
	spec.Progress.Report(builder.ProgressEvent{Phase: builder.PhaseDownload, Kind: builder.EventProgress, Done: 14_050_000, Total: 28_100_000, Unit: builder.UnitBytes})
	spec.Progress.Report(builder.ProgressEvent{Phase: builder.PhaseDownload, Kind: builder.EventPhaseFinished})
	if err := os.WriteFile(spec.TarballPath, []byte("tar"), 0o644); err != nil {
		return err
	}
	dpkgStatus := f.dpkgStatus
	if dpkgStatus == "" {
		dpkgStatus = fakeDpkgStatus
	}
	statusWritten := false
	for _, hook := range spec.CustomizeHooks {
		if listsParent, isCopyOut := strings.CutPrefix(hook, "copy-out /var/lib/apt/lists "); isCopyOut {
			listsDir := filepath.Join(strings.Trim(listsParent, "'"), "lists")
			if err := os.MkdirAll(listsDir, 0o755); err != nil {
				return err
			}
			// Named for the archive and suite of this build, as apt would.
			fields := strings.Fields(spec.SourceLines[0])
			_, archive, _ := strings.Cut(fields[1], "://")
			indexName := strings.ReplaceAll(archive, "/", "_") + "_dists_" + fields[2] + "_main_binary-amd64_Packages"
			if err := os.WriteFile(filepath.Join(listsDir, indexName), []byte(fakeAptIndex), 0o644); err != nil {
				return err
			}
		}
		if statusDestination, isDownload := strings.CutPrefix(hook, "download /var/lib/dpkg/status "); isDownload {
			if err := os.WriteFile(strings.Trim(statusDestination, "'"), []byte(dpkgStatus), 0o644); err != nil {
				return err
			}
			statusWritten = true
		}
	}
	if !statusWritten {
		return errors.New("no download hook for the dpkg status file")
	}
	return nil
}

// bootstrapRan reports whether the build reached the bootstrapper's Run.
func (f *fakeBootstrapper) bootstrapRan() bool {
	return f.lastSpec.Suite != ""
}

// newBuildApp returns an App that builds the recipe in recipeDir with
// bootstrapper, on Linux, outside WSL, with its work root in a temporary
// directory.
func newBuildApp(t *testing.T, recipeDir string, bootstrapper *fakeBootstrapper, stdout, stderr *bytes.Buffer) *App {
	t.Helper()
	cacheDir := t.TempDir()
	return &App{
		Stdout:    stdout,
		Stderr:    stderr,
		RecipeDir: recipeDir,
		GOOS:      "linux",
		Getenv: func(name string) string {
			if name == "XDG_CACHE_HOME" {
				return cacheDir
			}
			return ""
		},
		WSLPath: func(string) (string, error) { return "", errors.New("not running under WSL") },
		Builder: &builder.Builder{Bootstrapper: bootstrapper},
	}
}

// setRelease replaces the release in a copy of valid.toml.
func setRelease(t *testing.T, recipePath, release string) {
	t.Helper()
	recipeText, err := os.ReadFile(recipePath)
	if err != nil {
		t.Fatal(err)
	}
	updatedText := strings.Replace(string(recipeText), `release = "22.04"`, fmt.Sprintf("release = %q", release), 1)
	if updatedText == string(recipeText) {
		t.Fatal("fixture has no release line to replace")
	}
	if err := os.WriteFile(recipePath, []byte(updatedText), 0o644); err != nil {
		t.Fatal(err)
	}
}

// assertNoLock fails the test if a build left frostroot.lock in recipeDir.
func assertNoLock(t *testing.T, recipeDir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(recipeDir, "frostroot.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed build must not leave frostroot.lock (stat error: %v)", err)
	}
}

func TestBuildSuccess(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	var stdout, stderr bytes.Buffer
	app := newBuildApp(t, recipeDir, &fakeBootstrapper{}, &stdout, &stderr)
	if exitCode := app.Run([]string{"build"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	for _, wantFile := range []string{"frostroot.lock", filepath.Join("dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")} {
		if _, err := os.Stat(filepath.Join(recipeDir, wantFile)); err != nil {
			t.Error(err)
		}
	}
	for _, wantText := range []string{
		"wsl --import cpp-lab <install-dir> dist/cpp-lab-ubuntu-22.04-amd64.tar.gz",
		"frostroot.lock (1 package)",
		"frostroot vendor",
	} {
		if !strings.Contains(stdout.String(), wantText) {
			t.Errorf("stdout lacks %q:\n%s", wantText, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "PowerShell") {
		t.Errorf("no Windows path should be printed without wslpath:\n%s", stdout.String())
	}
	// Without a terminal, progress is plain lines: the phase, then tenths.
	for _, wantLine := range []string{"frostroot: building cpp-lab · Ubuntu 22.04 (jammy, amd64) · http://archive.ubuntu.com/ubuntu\n", "frostroot: Download packages\n", "frostroot:    50%  14.1 MB / 28.1 MB\n", "frostroot: Write frostroot.lock\n", "frostroot: Place tarball\n"} {
		if !strings.Contains(stderr.String(), wantLine) {
			t.Errorf("stderr lacks progress line %q:\n%s", wantLine, stderr.String())
		}
	}
	lock, err := recipe.LoadLock(filepath.Join(recipeDir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !lock.HasChecksums() {
		t.Errorf("the lock must record checksums for vendoring: %+v", lock.Packages)
	}
}

// vendorFakePool fills vendor/debs in recipeDir with a real .deb for every
// package of the lock and rewrites the lock's checksums to match, the way a
// vendor run after a build would leave things.
func vendorFakePool(t *testing.T, recipeDir string) recipe.Lockfile {
	t.Helper()
	lockPath := filepath.Join(recipeDir, "frostroot.lock")
	lock, err := recipe.LoadLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	poolDir := filepath.Join(recipeDir, "vendor", "debs")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for index, locked := range lock.Packages {
		path := debtest.Build(t, poolDir, filepath.Base(locked.Filename), ".zst", debtest.Control(locked.Name, locked.Version, locked.Arch))
		size, digest, err := deb.SHA256File(path)
		if err != nil {
			t.Fatal(err)
		}
		lock.Packages[index].Size, lock.Packages[index].SHA256 = size, digest
	}
	if err := recipe.SaveLock(lockPath, lock); err != nil {
		t.Fatal(err)
	}
	return lock
}

func TestBuildOffline(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	var stdout, stderr bytes.Buffer
	if exitCode := newBuildApp(t, recipeDir, &fakeBootstrapper{}, &stdout, &stderr).Run([]string{"build"}); exitCode != exitSuccess {
		t.Fatalf("online build: exit code = %d, stderr %s", exitCode, stderr.String())
	}
	vendorFakePool(t, recipeDir)
	lockBefore, err := os.ReadFile(filepath.Join(recipeDir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	bootstrapper := &fakeBootstrapper{}
	if exitCode := newBuildApp(t, recipeDir, bootstrapper, &stdout, &stderr).Run([]string{"build", "--offline"}); exitCode != exitSuccess {
		t.Fatalf("offline build: exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !bootstrapper.lastSpec.Trusted || len(bootstrapper.lastSpec.SourceLines) != 1 || !strings.HasPrefix(bootstrapper.lastSpec.SourceLines[0], "deb [trusted=yes] copy://") {
		t.Errorf("spec = %+v, want a trusted local repository", bootstrapper.lastSpec)
	}
	for _, wantText := range []string{"rebuilt from frostroot.lock: 1 package, every one as locked", "wsl --import cpp-lab"} {
		if !strings.Contains(stdout.String(), wantText) {
			t.Errorf("stdout lacks %q:\n%s", wantText, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "Wrote frostroot.lock") || strings.Contains(stdout.String(), "frostroot vendor\n") {
		t.Errorf("an offline build writes no lock and needs no vendoring hint:\n%s", stdout.String())
	}
	for _, wantLine := range []string{"frostroot: building cpp-lab · Ubuntu 22.04 (jammy, amd64) · vendor/debs\n", "frostroot: Check vendor/debs against frostroot.lock\n", "frostroot: Prepare the local package repository\n", "frostroot: Check the image against frostroot.lock\n"} {
		if !strings.Contains(stderr.String(), wantLine) {
			t.Errorf("stderr lacks %q:\n%s", wantLine, stderr.String())
		}
	}
	if strings.Contains(stderr.String(), "Write frostroot.lock") {
		t.Errorf("an offline build has no lock-writing phase:\n%s", stderr.String())
	}
	lockAfter, err := os.ReadFile(filepath.Join(recipeDir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if string(lockAfter) != string(lockBefore) {
		t.Error("the lock must not change")
	}
}

func TestBuildOfflineRefusals(t *testing.T) {
	testCases := []struct {
		name         string
		prepare      func(t *testing.T, recipeDir string)
		args         []string
		wantExit     int
		wantInStderr string
	}{
		{
			name:         "with --mirror",
			prepare:      func(*testing.T, string) {},
			args:         []string{"build", "--offline", "--mirror", "http://mirror.example/ubuntu"},
			wantExit:     exitUserError,
			wantInStderr: "exclude each other",
		},
		{
			name:         "no lock",
			prepare:      func(*testing.T, string) {},
			args:         []string{"build", "--offline"},
			wantExit:     exitUserError,
			wantInStderr: "no frostroot.lock",
		},
		{
			name: "pool missing",
			prepare: func(t *testing.T, recipeDir string) {
				t.Helper()
				var output bytes.Buffer
				if exitCode := newBuildApp(t, recipeDir, &fakeBootstrapper{}, &output, &output).Run([]string{"build"}); exitCode != exitSuccess {
					t.Fatalf("online build failed: %s", output.String())
				}
			},
			args:         []string{"build", "--offline"},
			wantExit:     exitUserError,
			wantInStderr: "run frostroot vendor",
		},
		{
			name: "image differs from the lock",
			prepare: func(t *testing.T, recipeDir string) {
				t.Helper()
				var output bytes.Buffer
				if exitCode := newBuildApp(t, recipeDir, &fakeBootstrapper{}, &output, &output).Run([]string{"build"}); exitCode != exitSuccess {
					t.Fatalf("online build failed: %s", output.String())
				}
				vendorFakePool(t, recipeDir)
			},
			args:         []string{"build", "--offline"},
			wantExit:     exitBuildFailed,
			wantInStderr: "differs from frostroot.lock",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recipeDir := newRecipeDir(t, "valid.toml")
			testCase.prepare(t, recipeDir)
			var stdout, stderr bytes.Buffer
			bootstrapper := &fakeBootstrapper{}
			if testCase.wantExit == exitBuildFailed {
				bootstrapper.dpkgStatus = "Package: git\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1:2\n"
			}
			if exitCode := newBuildApp(t, recipeDir, bootstrapper, &stdout, &stderr).Run(testCase.args); exitCode != testCase.wantExit {
				t.Fatalf("exit code = %d, want %d; stderr %s", exitCode, testCase.wantExit, stderr.String())
			}
			if !strings.Contains(stderr.String(), testCase.wantInStderr) {
				t.Errorf("stderr lacks %q:\n%s", testCase.wantInStderr, stderr.String())
			}
			if testCase.wantExit == exitUserError && bootstrapper.bootstrapRan() {
				t.Error("a refused build must not reach the bootstrapper")
			}
		})
	}
}

func TestBuildPrintsWindowsPathUnderWSL(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	var stdout, stderr bytes.Buffer
	app := newBuildApp(t, recipeDir, &fakeBootstrapper{}, &stdout, &stderr)
	var convertedPath string
	app.WSLPath = func(linuxPath string) (string, error) {
		convertedPath = linuxPath
		return `\\wsl.localhost\Ubuntu\home\o'neil\lab\dist\cpp-lab-ubuntu-22.04-amd64.tar.gz`, nil
	}
	if exitCode := app.Run([]string{"build"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if wantPath := filepath.Join(recipeDir, "dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz"); convertedPath != wantPath {
		t.Errorf("WSLPath converted %q, want %q", convertedPath, wantPath)
	}
	// Single-quoted for PowerShell, with the embedded quote doubled.
	wantCommand := `wsl --import cpp-lab <install-dir> '\\wsl.localhost\Ubuntu\home\o''neil\lab\dist\cpp-lab-ubuntu-22.04-amd64.tar.gz'`
	if !strings.Contains(stdout.String(), wantCommand) {
		t.Errorf("stdout lacks %q:\n%s", wantCommand, stdout.String())
	}
}

func TestBuildWarnsOnEndOfLifeRelease(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	setRelease(t, filepath.Join(recipeDir, "frostroot.toml"), "20.04")
	var stdout, stderr bytes.Buffer
	app := newBuildApp(t, recipeDir, &fakeBootstrapper{}, &stdout, &stderr)
	if exitCode := app.Run([]string{"build"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	lowercaseStderr := strings.ToLower(stderr.String())
	for _, wantText := range []string{"20.04", "security", "ubuntu pro"} {
		if !strings.Contains(lowercaseStderr, wantText) {
			t.Errorf("end-of-life warning should mention %q:\n%s", wantText, stderr.String())
		}
	}
}

func TestBuildDoesNotWarnOnSupportedRelease(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := newBuildApp(t, newRecipeDir(t, "valid.toml"), &fakeBootstrapper{}, &stdout, &stderr)
	if exitCode := app.Run([]string{"build"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if strings.Contains(strings.ToLower(stderr.String()), "warning") {
		t.Errorf("no warning expected for 22.04:\n%s", stderr.String())
	}
}

func TestBuildOutsideLinuxPointsAtWSL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	bootstrapper := &fakeBootstrapper{}
	app := newBuildApp(t, newRecipeDir(t, "valid.toml"), bootstrapper, &stdout, &stderr)
	app.GOOS = "windows"
	if exitCode := app.Run([]string{"build"}); exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
	if !strings.Contains(stderr.String(), "WSL") {
		t.Errorf("message should point Windows users at WSL:\n%s", stderr.String())
	}
	if bootstrapper.bootstrapRan() {
		t.Error("the bootstrap must not run")
	}
}

func TestBuildUnwritableOutputIsUserError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	recipeDir := newRecipeDir(t, "valid.toml")
	distDir := filepath.Join(recipeDir, "dist")
	if err := os.Mkdir(distDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(distDir, 0o755); err != nil {
			t.Error(err)
		}
	})
	var stdout, stderr bytes.Buffer
	bootstrapper := &fakeBootstrapper{}
	app := newBuildApp(t, recipeDir, bootstrapper, &stdout, &stderr)
	if exitCode := app.Run([]string{"build"}); exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d; stderr %s", exitCode, exitUserError, stderr.String())
	}
	if !strings.Contains(stderr.String(), "sudo") {
		t.Errorf("stderr should explain the ownership problem:\n%s", stderr.String())
	}
	if bootstrapper.bootstrapRan() {
		t.Error("the problem must be reported before bootstrapping")
	}
}

func TestBuildPreflightFailuresAreUserErrors(t *testing.T) {
	preflightErrors := []error{
		fmt.Errorf("%w; install it with: sudo apt install mmdebstrap", builder.ErrNoMmdebstrap),
		fmt.Errorf("%w at /usr/share/keyrings/ubuntu-archive-keyring.gpg", builder.ErrNoKeyring),
	}
	for _, preflightErr := range preflightErrors {
		t.Run(preflightErr.Error(), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			app := newBuildApp(t, newRecipeDir(t, "valid.toml"), &fakeBootstrapper{preflightErr: preflightErr}, &stdout, &stderr)
			if exitCode := app.Run([]string{"build"}); exitCode != exitUserError {
				t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
			}
			if !strings.Contains(stderr.String(), preflightErr.Error()) {
				t.Errorf("stderr should carry the explanation:\n%s", stderr.String())
			}
		})
	}
}

func TestBuildWorkRootUnderMntIsUserError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := newBuildApp(t, newRecipeDir(t, "valid.toml"), &fakeBootstrapper{}, &stdout, &stderr)
	app.Getenv = func(name string) string {
		if name == "XDG_CACHE_HOME" {
			return "/mnt/c/cache"
		}
		return ""
	}
	if exitCode := app.Run([]string{"build"}); exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d; stderr %s", exitCode, exitUserError, stderr.String())
	}
}

func TestBuildBootstrapFailure(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	var stdout, stderr bytes.Buffer
	app := newBuildApp(t, recipeDir, &fakeBootstrapper{runErr: errors.New("mmdebstrap exploded")}, &stdout, &stderr)
	if exitCode := app.Run([]string{"build"}); exitCode != exitBuildFailed {
		t.Fatalf("exit code = %d, want %d; stderr %s", exitCode, exitBuildFailed, stderr.String())
	}
	for _, wantText := range []string{"mmdebstrap exploded", "work directory kept"} {
		if !strings.Contains(stderr.String(), wantText) {
			t.Errorf("stderr lacks %q:\n%s", wantText, stderr.String())
		}
	}
	assertNoLock(t, recipeDir)
}

func TestBuildArchiveTroubleSuggestsMirrorFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	bootstrapper := &fakeBootstrapper{runErr: errors.New("mmdebstrap failed: exit status 1\n" +
		"E: Failed to fetch http://archive.ubuntu.com/ubuntu/dists/jammy/InRelease  Temporary failure resolving 'archive.ubuntu.com'")}
	app := newBuildApp(t, newRecipeDir(t, "valid.toml"), bootstrapper, &stdout, &stderr)
	if exitCode := app.Run([]string{"build"}); exitCode != exitBuildFailed {
		t.Fatalf("exit code = %d, want %d", exitCode, exitBuildFailed)
	}
	for _, wantText := range []string{"--mirror", "http://archive.ubuntu.com/ubuntu"} {
		if !strings.Contains(stderr.String(), wantText) {
			t.Errorf("stderr should name the archive and suggest --mirror, lacks %q:\n%s", wantText, stderr.String())
		}
	}
}

func TestBuildInterrupted(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	var stdout, stderr bytes.Buffer
	app := newBuildApp(t, recipeDir, &fakeBootstrapper{runErr: fmt.Errorf("mmdebstrap: %w", context.Canceled)}, &stdout, &stderr)
	if exitCode := app.Run([]string{"build"}); exitCode != exitInterrupted {
		t.Fatalf("exit code = %d, want %d", exitCode, exitInterrupted)
	}
	assertNoLock(t, recipeDir)
	if !strings.Contains(stderr.String(), "work directory kept") {
		t.Errorf("an interrupt keeps the work directory like any failure:\n%s", stderr.String())
	}
}

func TestBuildInvalidRecipeNeverBootstraps(t *testing.T) {
	var stdout, stderr bytes.Buffer
	bootstrapper := &fakeBootstrapper{}
	app := newBuildApp(t, newRecipeDir(t, "bad-user.toml"), bootstrapper, &stdout, &stderr)
	if exitCode := app.Run([]string{"build"}); exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
	if bootstrapper.bootstrapRan() {
		t.Error("an invalid recipe must never reach the bootstrapper")
	}
}

func TestBuildWithoutRecipe(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := newBuildApp(t, t.TempDir(), &fakeBootstrapper{}, &stdout, &stderr)
	if exitCode := app.Run([]string{"build"}); exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
}

func TestBuildMirrorFlagReachesBuilder(t *testing.T) {
	argumentLists := [][]string{
		{"build", "--mirror", "http://mirror.example/ubuntu"},
		{"build", "--mirror=https://mirror.example/ubuntu"},
	}
	for _, args := range argumentLists {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			bootstrapper := &fakeBootstrapper{}
			var stdout, stderr bytes.Buffer
			app := newBuildApp(t, newRecipeDir(t, "valid.toml"), bootstrapper, &stdout, &stderr)
			if exitCode := app.Run(args); exitCode != exitSuccess {
				t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
			}
			sourceLines := bootstrapper.lastSpec.SourceLines
			if len(sourceLines) != 3 {
				t.Fatalf("source lines = %#v, want 3", sourceLines)
			}
			for _, sourceLine := range sourceLines {
				if !strings.Contains(sourceLine, "mirror.example") {
					t.Errorf("mirror not applied to %q", sourceLine)
				}
			}
		})
	}
}

func TestBuildRejectsInvalidMirror(t *testing.T) {
	invalidMirrorURLs := []string{"ftp://mirror.example/ubuntu", "mirror.example/ubuntu", "http://", "http://mirror.example/ubu ntu"}
	for _, mirrorURL := range invalidMirrorURLs {
		t.Run(mirrorURL, func(t *testing.T) {
			bootstrapper := &fakeBootstrapper{}
			var stdout, stderr bytes.Buffer
			app := newBuildApp(t, newRecipeDir(t, "valid.toml"), bootstrapper, &stdout, &stderr)
			if exitCode := app.Run([]string{"build", "--mirror", mirrorURL}); exitCode != exitUserError {
				t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
			}
			if bootstrapper.bootstrapRan() {
				t.Error("the bootstrap must not run")
			}
		})
	}
}

func TestBuildKeepWorkPrintsWorkDir(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := newBuildApp(t, newRecipeDir(t, "valid.toml"), &fakeBootstrapper{}, &stdout, &stderr)
	if exitCode := app.Run([]string{"build", "--keep-work"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String()+stderr.String(), "work directory kept") {
		t.Errorf("--keep-work should say where the work directory is:\n%s%s", stdout.String(), stderr.String())
	}
}

func TestBuildRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"build", "--bogus"}, {"build", "extra"}} {
		var stdout, stderr bytes.Buffer
		app := newBuildApp(t, newRecipeDir(t, "valid.toml"), &fakeBootstrapper{}, &stdout, &stderr)
		if exitCode := app.Run(args); exitCode != exitUserError {
			t.Errorf("Run(%q) = %d, want %d", args, exitCode, exitUserError)
		}
	}
}

// blockedReader never delivers input, like a terminal nobody types on.
type blockedReader struct{}

func (blockedReader) Read([]byte) (int, error) { select {} }

func TestBuildFullScreenThenPlainSummary(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	var stdout, stderr bytes.Buffer
	app := newBuildApp(t, recipeDir, &fakeBootstrapper{}, &stdout, &stderr)
	app.Stdin = blockedReader{}
	app.IsTerminal = terminalChecker(true)
	app.Getenv = func(name string) string {
		switch name {
		case "XDG_CACHE_HOME":
			return t.TempDir()
		case "TERM":
			return "xterm-256color"
		}
		return ""
	}
	if exitCode := app.Run([]string{"build"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	output := stdout.String()
	// The screen ran (alternate screen on, phases drawn) and the plain
	// summary followed it.
	for _, wantText := range []string{"\x1b[?1049h", "Download packages", "Write frostroot.lock", "Wrote frostroot.lock (1 package)", "wsl --import cpp-lab"} {
		if !strings.Contains(output, wantText) {
			t.Errorf("stdout lacks %q", wantText)
		}
	}
	if strings.Contains(stderr.String(), "frostroot: Download packages") {
		t.Error("the full-screen build must not also print plain progress lines")
	}
}

func TestFormatMegabytes(t *testing.T) {
	testCases := []struct {
		sizeInBytes int64
		want        string
	}{
		{sizeInBytes: 0, want: "0 MB"},
		{sizeInBytes: megabyte/2 - 1, want: "0 MB"},
		{sizeInBytes: megabyte / 2, want: "1 MB"},
		{sizeInBytes: 368 * megabyte, want: "368 MB"},
	}
	for _, testCase := range testCases {
		if got := formatMegabytes(testCase.sizeInBytes); got != testCase.want {
			t.Errorf("formatMegabytes(%d) = %q, want %q", testCase.sizeInBytes, got, testCase.want)
		}
	}
}
