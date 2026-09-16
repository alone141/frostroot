package builder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"frostroot/internal/recipe"
)

// sampleDpkgStatus lists two installed packages, deliberately out of order.
const sampleDpkgStatus = "Package: libc6\nStatus: install ok installed\nArchitecture: amd64\nVersion: 2.35-0ubuntu3.8\n\n" +
	"Package: git\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1:2.34.1-1ubuntu1.11\n"

// fakeBootstrapper produces the same two files the real bootstrapper does: a
// tarball at TarballPath and a dpkg status file wherever the download hook
// says. It does not run hooks.
type fakeBootstrapper struct {
	runErr       error  // returned by Run instead of producing files
	dpkgStatus   string // written as the status file; defaults to sampleDpkgStatus
	skipTarball  bool
	emptyTarball bool // mmdebstrap creates its output file before it starts

	runCount          int
	lastSpec          BootstrapSpec
	stageFilesPresent map[string]bool // stage file name to whether it existed when Run was called
}

func (f *fakeBootstrapper) Run(_ context.Context, spec BootstrapSpec) error {
	f.runCount++
	f.lastSpec = spec
	f.stageFilesPresent = map[string]bool{}
	for _, stageFileName := range []string{"wsl.conf", "sudoers", "provision.sh"} {
		_, err := os.Stat(filepath.Join(spec.WorkDir, "stage", stageFileName))
		f.stageFilesPresent[stageFileName] = err == nil
	}
	if f.runErr != nil {
		return f.runErr
	}
	switch {
	case f.emptyTarball:
		if err := os.WriteFile(spec.TarballPath, nil, 0o644); err != nil {
			return err
		}
	case !f.skipTarball:
		if err := os.WriteFile(spec.TarballPath, []byte("tarball"), 0o644); err != nil {
			return err
		}
	}
	dpkgStatus := f.dpkgStatus
	if dpkgStatus == "" {
		dpkgStatus = sampleDpkgStatus
	}
	return os.WriteFile(dpkgStatusPathFromHooks(spec.CustomizeHooks), []byte(dpkgStatus), 0o644)
}

// dpkgStatusPathFromHooks finds where the download hook writes the status
// file, the way mmdebstrap would.
func dpkgStatusPathFromHooks(hooks []string) string {
	const downloadPrefix = "download /var/lib/dpkg/status "
	for _, hook := range hooks {
		if quotedPath, isDownload := strings.CutPrefix(hook, downloadPrefix); isDownload {
			return strings.Trim(quotedPath, "'")
		}
	}
	return ""
}

// preflightingBootstrapper is a fakeBootstrapper that also implements
// Preflighter.
type preflightingBootstrapper struct {
	fakeBootstrapper
	preflightErr  error
	preflightSpec BootstrapSpec
}

func (p *preflightingBootstrapper) Preflight(spec BootstrapSpec) error {
	p.preflightSpec = spec
	return p.preflightErr
}

// newTestOptions returns build options that keep the work root inside the
// test's temporary directory, and that work root.
func newTestOptions(t *testing.T) (options Options, workRoot string) {
	t.Helper()
	cacheHome := t.TempDir()
	options = Options{
		RecipeDir: t.TempDir(),
		GOOS:      "linux",
		Getenv:    fakeEnvironment(map[string]string{"XDG_CACHE_HOME": cacheHome}),
	}
	return options, filepath.Join(cacheHome, "frostroot")
}

// expectedTarballPath is where a build of sampleRecipe places its tarball.
func expectedTarballPath(options Options) string {
	return filepath.Join(options.RecipeDir, "dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")
}

// buildWith builds sampleRecipe with bootstrapper and options.
func buildWith(bootstrapper Bootstrapper, options Options) (Result, error) {
	return (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), sampleRecipe(), options)
}

// assertNoTemporaryFiles fails if a temporary file was left in the recipe
// directory or dist/. The * in filepath.Glob matches a leading dot, so hidden
// files are covered too.
func assertNoTemporaryFiles(t *testing.T, recipeDir string) {
	t.Helper()
	for _, pattern := range []string{"*.tmp", filepath.Join("dist", "*.tmp")} {
		leftovers, err := filepath.Glob(filepath.Join(recipeDir, pattern))
		if err != nil {
			t.Fatal(err)
		}
		if len(leftovers) != 0 {
			t.Fatalf("temporary files left behind: %q", leftovers)
		}
	}
}

// assertNoBuildOutput fails if a failed build left a lock, a tarball or a
// temporary file.
func assertNoBuildOutput(t *testing.T, options Options) {
	t.Helper()
	for _, path := range []string{filepath.Join(options.RecipeDir, "frostroot.lock"), expectedTarballPath(options)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s must not exist after a failed build, Stat error = %v", path, err)
		}
	}
	assertNoTemporaryFiles(t, options.RecipeDir)
}

// assertNotCreated fails if path exists.
func assertNotCreated(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s should not have been created, Stat error = %v", path, err)
	}
}

func TestBuildSuccessWritesLockAndTarball(t *testing.T) {
	options, workRoot := newTestOptions(t)
	bootstrapper := &fakeBootstrapper{}
	result, err := buildWith(bootstrapper, options)
	if err != nil {
		t.Fatal(err)
	}

	spec := bootstrapper.lastSpec
	if spec.Suite != "jammy" || spec.Arch != "amd64" || !spec.InstallRecommends {
		t.Errorf("spec = %+v, want jammy, amd64 and Recommends", spec)
	}
	if spec.KeyringPath != "/usr/share/keyrings/ubuntu-archive-keyring.gpg" {
		t.Errorf("KeyringPath = %q", spec.KeyringPath)
	}
	wantSourceLines := []string{
		"deb http://archive.ubuntu.com/ubuntu jammy main universe",
		"deb http://archive.ubuntu.com/ubuntu jammy-updates main universe",
		"deb http://archive.ubuntu.com/ubuntu jammy-security main universe",
	}
	if !slices.Equal(spec.SourceLines, wantSourceLines) {
		t.Errorf("SourceLines = %q, want the three pockets", spec.SourceLines)
	}
	for _, wantPackage := range []string{"git", "build-essential", "cmake", "systemd", "systemd-sysv", "sudo", "ca-certificates"} {
		if !slices.Contains(spec.Include, wantPackage) {
			t.Errorf("Include = %q, missing %s", spec.Include, wantPackage)
		}
	}
	if !strings.HasPrefix(spec.WorkDir, workRoot+string(filepath.Separator)) {
		t.Errorf("WorkDir = %q, want a directory under %q", spec.WorkDir, workRoot)
	}
	if spec.TarballPath != filepath.Join(spec.WorkDir, "image.tar.gz") {
		t.Errorf("TarballPath = %q, want image.tar.gz in the work directory", spec.TarballPath)
	}
	for stageFileName, present := range bootstrapper.stageFilesPresent {
		if !present {
			t.Errorf("stage file %s must exist before the bootstrap runs", stageFileName)
		}
	}
	if len(spec.CustomizeHooks) != 4 {
		t.Errorf("CustomizeHooks = %q, want upload, upload, provision, download", spec.CustomizeHooks)
	}

	lock, err := recipe.LoadLock(filepath.Join(options.RecipeDir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	wantLock := recipe.Lockfile{
		Version:          1,
		Distro:           "ubuntu",
		Release:          "22.04",
		Suite:            "jammy",
		Arch:             "amd64",
		Mirror:           "http://archive.ubuntu.com/ubuntu",
		Sources:          wantSourceLines,
		FrostrootVersion: Version,
		Requested:        []string{"git", "build-essential", "cmake"},
		Packages: []recipe.LockPackage{ // every installed package, sorted
			{Name: "git", Version: "1:2.34.1-1ubuntu1.11", Arch: "amd64"},
			{Name: "libc6", Version: "2.35-0ubuntu3.8", Arch: "amd64"},
		},
	}
	if !slices.Equal(lock.Sources, wantLock.Sources) || !slices.Equal(lock.Requested, wantLock.Requested) ||
		!slices.Equal(lock.Packages, wantLock.Packages) {
		t.Errorf("lock =\n%+v\nwant\n%+v", lock, wantLock)
	}
	if lock.Version != wantLock.Version || lock.Distro != wantLock.Distro || lock.Release != wantLock.Release ||
		lock.Suite != wantLock.Suite || lock.Arch != wantLock.Arch || lock.Mirror != wantLock.Mirror ||
		lock.FrostrootVersion != wantLock.FrostrootVersion {
		t.Errorf("lock header =\n%+v\nwant\n%+v", lock, wantLock)
	}

	tarballContent, err := os.ReadFile(expectedTarballPath(options))
	if err != nil {
		t.Fatal(err)
	}
	if string(tarballContent) != "tarball" {
		t.Errorf("placed tarball holds %q, want the bootstrapper's output", tarballContent)
	}
	wantResult := Result{
		LockPath:              filepath.Join(options.RecipeDir, "frostroot.lock"),
		TarballPath:           expectedTarballPath(options),
		InstalledPackageCount: 2,
	}
	if result != wantResult {
		t.Errorf("Result = %+v, want %+v", result, wantResult)
	}
	assertNotCreated(t, spec.WorkDir) // removed after success
	assertNoTemporaryFiles(t, options.RecipeDir)
}

func TestBuildsInOneDirectoryDoNotShareTemporaryFiles(t *testing.T) {
	// Two builds in the same recipe directory: neither a failing nor a
	// succeeding build may remove the other's temporary lock. (Concurrent
	// builds are still last-writer-wins; this only keeps them from sabotaging
	// each other.)
	options, _ := newTestOptions(t)
	otherBuildsLock := filepath.Join(options.RecipeDir, ".frostroot.lock.424242.tmp")
	if err := os.WriteFile(otherBuildsLock, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertStillTheirs := func(situation string) {
		t.Helper()
		content, err := os.ReadFile(otherBuildsLock)
		if err != nil || string(content) != "theirs" {
			t.Fatalf("another build's temporary lock was touched by %s: %q, %v", situation, content, err)
		}
	}

	if _, err := buildWith(&fakeBootstrapper{skipTarball: true}, options); err == nil {
		t.Fatal("build without a tarball succeeded, want a failure")
	}
	assertStillTheirs("a failed build")
	if _, err := buildWith(&fakeBootstrapper{}, options); err != nil {
		t.Fatal(err)
	}
	assertStillTheirs("a successful build")
}

func TestBuildRefusesUnwritableOutputBeforeBootstrapping(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	options, workRoot := newTestOptions(t)
	// dist/ as a sudo build leaves it: not writable by this user.
	distDir := filepath.Join(options.RecipeDir, "dist")
	if err := os.Mkdir(distDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(distDir, 0o755) })

	bootstrapper := &fakeBootstrapper{}
	result, err := buildWith(bootstrapper, options)
	if !errors.Is(err, ErrUnwritableOutput) {
		t.Fatalf("Build error = %v, want ErrUnwritableOutput", err)
	}
	if bootstrapper.runCount != 0 || result.WorkDir != "" {
		t.Fatalf("a build that could never be placed must not start: runs = %d, Result = %+v", bootstrapper.runCount, result)
	}
	assertNotCreated(t, workRoot)
}

func TestBuildRequestedPackagesExcludeEssentials(t *testing.T) {
	options, _ := newTestOptions(t)
	if _, err := buildWith(&fakeBootstrapper{}, options); err != nil {
		t.Fatal(err)
	}
	lock, err := recipe.LoadLock(filepath.Join(options.RecipeDir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(lock.Requested, "systemd") {
		t.Fatalf("Requested = %q, want the recipe's list as written", lock.Requested)
	}
}

func TestBuildWithoutSudoStagesNoSudoers(t *testing.T) {
	options, _ := newTestOptions(t)
	bootstrapper := &fakeBootstrapper{}
	imageRecipe := sampleRecipe()
	imageRecipe.User.Sudo = false
	if _, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), imageRecipe, options); err != nil {
		t.Fatal(err)
	}
	if bootstrapper.stageFilesPresent["sudoers"] {
		t.Error("a sudoers file was staged for a user without sudo")
	}
	if strings.Contains(strings.Join(bootstrapper.lastSpec.CustomizeHooks, "\n"), "sudoers") {
		t.Errorf("hooks mention sudoers for a user without sudo: %q", bootstrapper.lastSpec.CustomizeHooks)
	}
}

func TestBuildOverwritesExistingOutput(t *testing.T) {
	options, _ := newTestOptions(t)
	lockPath := filepath.Join(options.RecipeDir, "frostroot.lock")
	if err := os.MkdirAll(filepath.Dir(expectedTarballPath(options)), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{expectedTarballPath(options), lockPath} {
		if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := buildWith(&fakeBootstrapper{}, options); err != nil {
		t.Fatal(err)
	}
	tarballContent, err := os.ReadFile(expectedTarballPath(options))
	if err != nil {
		t.Fatal(err)
	}
	if string(tarballContent) != "tarball" {
		t.Errorf("tarball not replaced: holds %q", tarballContent)
	}
	if _, err := recipe.LoadLock(lockPath); err != nil {
		t.Errorf("lock not replaced: %v", err)
	}
}

func TestBuildKeepWork(t *testing.T) {
	options, _ := newTestOptions(t)
	options.KeepWork = true
	result, err := buildWith(&fakeBootstrapper{}, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkDir == "" {
		t.Fatal("Result.WorkDir is empty, want the kept work directory")
	}
	if _, err := os.Stat(filepath.Join(result.WorkDir, "stage", "dpkg-status")); err != nil {
		t.Fatalf("the kept work directory should still hold the stage: %v", err)
	}
}

func TestBuildFailuresKeepWorkDirAndWriteNothing(t *testing.T) {
	testCases := []struct {
		name         string
		bootstrapper *fakeBootstrapper
		wantInError  string
	}{
		{
			name:         "bootstrap fails",
			bootstrapper: &fakeBootstrapper{runErr: errors.New("mmdebstrap exploded")},
			wantInError:  "mmdebstrap exploded",
		},
		{
			name:         "tarball missing",
			bootstrapper: &fakeBootstrapper{skipTarball: true},
			wantInError:  "no tarball",
		},
		{
			// mmdebstrap creates the output file at 0 bytes before it starts.
			name:         "tarball empty",
			bootstrapper: &fakeBootstrapper{emptyTarball: true},
			wantInError:  "no tarball",
		},
		{
			// A bootstrap that installs nothing must not produce a lock.
			name:         "dpkg status empty",
			bootstrapper: &fakeBootstrapper{dpkgStatus: "\n"},
			wantInError:  "no installed packages",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			options, _ := newTestOptions(t)
			result, err := buildWith(testCase.bootstrapper, options)
			if err == nil || !strings.Contains(err.Error(), testCase.wantInError) {
				t.Fatalf("Build error = %v, want one mentioning %q", err, testCase.wantInError)
			}
			if result.WorkDir == "" {
				t.Fatal("Result.WorkDir is empty, want the kept work directory")
			}
			if _, err := os.Stat(result.WorkDir); err != nil {
				t.Fatalf("the work directory must be kept for debugging: %v", err)
			}
			assertNoBuildOutput(t, options)
		})
	}
}

func TestBuildMirrorReplacesArchiveInAllPockets(t *testing.T) {
	options, _ := newTestOptions(t)
	options.MirrorURL = "http://mirror.example/ubuntu"
	bootstrapper := &fakeBootstrapper{}
	if _, err := buildWith(bootstrapper, options); err != nil {
		t.Fatal(err)
	}
	for _, sourceLine := range bootstrapper.lastSpec.SourceLines {
		if !strings.Contains(sourceLine, "mirror.example") {
			t.Errorf("source line %q does not use the mirror", sourceLine)
		}
	}
	lock, err := recipe.LoadLock(filepath.Join(options.RecipeDir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Mirror != "http://mirror.example/ubuntu" || !strings.Contains(lock.Sources[0], "mirror.example") {
		t.Errorf("the lock must record the mirror actually used: %+v", lock)
	}
}

func TestBuildStopsBeforeAnyWork(t *testing.T) {
	testCases := []struct {
		name         string
		adjust       func(*Options)
		bootstrapper Bootstrapper
		wantError    error
	}{
		{
			name:         "not Linux",
			adjust:       func(options *Options) { options.GOOS = "windows" },
			bootstrapper: &fakeBootstrapper{},
			wantError:    ErrNotLinux,
		},
		{
			name: "work root under /mnt",
			adjust: func(options *Options) {
				options.Getenv = fakeEnvironment(map[string]string{"XDG_CACHE_HOME": "/mnt/c/cache"})
			},
			bootstrapper: &fakeBootstrapper{},
			wantError:    ErrBadWorkRoot,
		},
		{
			name:         "preflight fails",
			adjust:       func(*Options) {},
			bootstrapper: &preflightingBootstrapper{preflightErr: ErrNoKeyring},
			wantError:    ErrNoKeyring,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			options, workRoot := newTestOptions(t)
			testCase.adjust(&options)
			result, err := buildWith(testCase.bootstrapper, options)
			if !errors.Is(err, testCase.wantError) {
				t.Fatalf("Build error = %v, want %v", err, testCase.wantError)
			}
			if result.WorkDir != "" {
				t.Errorf("Result.WorkDir = %q, want none", result.WorkDir)
			}
			assertNotCreated(t, workRoot)
		})
	}
}

func TestBuildWithUnknownReleaseDoesNotBootstrap(t *testing.T) {
	options, _ := newTestOptions(t)
	bootstrapper := &fakeBootstrapper{}
	imageRecipe := sampleRecipe()
	imageRecipe.Image.Release = "18.04"
	_, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), imageRecipe, options)
	if err == nil || bootstrapper.runCount != 0 {
		t.Fatalf("Build error = %v, runs = %d; want an error and no bootstrap", err, bootstrapper.runCount)
	}
}

func TestBuildPreflightSeesTheWorkRoot(t *testing.T) {
	// The real preflight checks that mmdebstrap's user namespace can reach
	// the work root, so it needs to know where that is.
	options, workRoot := newTestOptions(t)
	bootstrapper := &preflightingBootstrapper{}
	if _, err := buildWith(bootstrapper, options); err != nil {
		t.Fatal(err)
	}
	preflightSpec := bootstrapper.preflightSpec
	if preflightSpec.WorkDir != workRoot || preflightSpec.KeyringPath != UbuntuArchiveKeyring || len(preflightSpec.SourceLines) != 3 {
		t.Fatalf("preflight spec = %+v, want WorkDir %q, the archive keyring and three pockets", preflightSpec, workRoot)
	}
	if bootstrapper.runCount != 1 {
		t.Fatalf("runs = %d, want the bootstrap to run once after a clean preflight", bootstrapper.runCount)
	}
}

func TestBuildWorkDirsReachableWhateverTheUmask(t *testing.T) {
	// In unshare mode mmdebstrap's root is a subordinate uid, "other" to the
	// user's files, and it reads the provision script from the stage directory.
	previousUmask := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previousUmask) })

	options, workRoot := newTestOptions(t)
	options.KeepWork = true
	result, err := buildWith(&fakeBootstrapper{}, options)
	if err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(result.WorkDir, "stage")
	wantModes := map[string]os.FileMode{
		workRoot:                                0o755,
		result.WorkDir:                          0o755,
		stageDir:                                0o755,
		filepath.Join(stageDir, "provision.sh"): 0o644,
		filepath.Join(stageDir, "wsl.conf"):     0o644,
		filepath.Join(stageDir, "sudoers"):      0o644,
	}
	for path, wantMode := range wantModes {
		pathInfo, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := pathInfo.Mode().Perm(); mode != wantMode {
			t.Errorf("%s: mode = %v, want %v", path, mode, wantMode)
		}
	}
}

func TestBuildFocalUsesArchivePockets(t *testing.T) {
	options, _ := newTestOptions(t)
	bootstrapper := &fakeBootstrapper{}
	imageRecipe := sampleRecipe()
	imageRecipe.Image.Release = "20.04"
	if _, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), imageRecipe, options); err != nil {
		t.Fatal(err)
	}
	if bootstrapper.lastSpec.Suite != "focal" {
		t.Errorf("Suite = %q, want focal", bootstrapper.lastSpec.Suite)
	}
	// Task 0 spike: focal is not on old-releases; every pocket returns 404
	// there.
	for _, sourceLine := range bootstrapper.lastSpec.SourceLines {
		if !strings.HasPrefix(sourceLine, "deb http://archive.ubuntu.com/ubuntu focal") {
			t.Errorf("source line %q, want focal from the archive", sourceLine)
		}
	}
	if _, err := os.Stat(filepath.Join(options.RecipeDir, "dist", "cpp-lab-ubuntu-20.04-amd64.tar.gz")); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCanceledKeepsWorkDirAndWritesNothing(t *testing.T) {
	options, _ := newTestOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	builder := &Builder{Bootstrapper: &fakeBootstrapper{runErr: context.Canceled}}
	result, err := builder.Build(ctx, sampleRecipe(), options)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Build error = %v, want context.Canceled", err)
	}
	if result.WorkDir == "" {
		t.Fatal("an interrupted build keeps its work directory like any other failure")
	}
	assertNoBuildOutput(t, options)
}
