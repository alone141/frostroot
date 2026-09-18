package builder

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"frostroot/internal/deb"
	"frostroot/internal/deb/debtest"
	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// sampleDpkgStatus lists two installed packages, deliberately out of order.
const sampleDpkgStatus = "Package: libc6\nStatus: install ok installed\nArchitecture: amd64\nVersion: 2.35-0ubuntu3.8\n\n" +
	"Package: git\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1:2.34.1-1ubuntu1.11\n"

// sampleExtendedStates is apt's record for the sample image: libc6 came in
// as a dependency, git was asked for.
const sampleExtendedStates = "Package: libc6\nArchitecture: amd64\nAuto-Installed: 1\n\n"

// sampleEpoch is a fixed instant tests freeze images at: 2025-09-17 00:00:00 UTC.
const sampleEpoch = "1758067200"

// Checksums the sample apt indexes record for the sample packages.
const (
	sampleGitSHA256   = "af7af42226d21bcbf87cf62a9e158bd5dfbd051ebbde8818ca441dc8085f67af"
	sampleLibc6SHA256 = "af36c7ac770770fe3d3c10e85d6bc538e76e57570ba7db7d397fb9f654783ef3"
)

// sampleAptLists returns the Packages indexes a jammy bootstrap leaves in
// /var/lib/apt/lists, by file name, for the sample packages: git in the
// -updates pocket, libc6 in the release pocket.
func sampleAptLists() map[string]string {
	return map[string]string{
		"archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages": "Package: libc6\nArchitecture: amd64\nVersion: 2.35-0ubuntu3.8\nPriority: required\n" +
			"Filename: pool/main/g/glibc/libc6_2.35-0ubuntu3.8_amd64.deb\nSize: 3235712\nSHA256: " + sampleLibc6SHA256 + "\n\n" +
			"Package: git\nArchitecture: amd64\nVersion: 1:2.34.1-1ubuntu1\nFilename: pool/main/g/git/git_1%3a2.34.1-1ubuntu1_amd64.deb\nSize: 3100000\nSHA256: 0000000000000000000000000000000000000000000000000000000000000000\n",
		"archive.ubuntu.com_ubuntu_dists_jammy-updates_main_binary-amd64_Packages": "Package: git\nArchitecture: amd64\nVersion: 1:2.34.1-1ubuntu1.11\n" +
			"Filename: pool/main/g/git/git_1%3a2.34.1-1ubuntu1.11_amd64.deb\nSize: 3165964\nSHA256: " + sampleGitSHA256 + "\n",
		"archive.ubuntu.com_ubuntu_dists_jammy-updates_InRelease": "Origin: Ubuntu\n",
	}
}

// fakeBootstrapper produces the files the real bootstrapper does: a tarball
// at TarballPath, a dpkg status file and an extended_states where the
// download hooks say, and, when a copy-out hook asks for the apt lists, a
// lists directory. It does not run hooks.
type fakeBootstrapper struct {
	runErr             error             // returned by Run instead of producing files
	dpkgStatus         string            // written as the status file; defaults to sampleDpkgStatus
	extendedStates     string            // written as apt's extended_states; defaults to sampleExtendedStates
	aptLists           map[string]string // written as the lists directory; defaults to sampleAptLists()
	skipAptLists       bool              // leave no lists directory even when asked
	skipExtendedStates bool              // leave no extended_states even when asked
	skipTarball        bool
	emptyTarball       bool // mmdebstrap creates its output file before it starts

	runCount          int
	lastSpec          BootstrapSpec
	stageFilesPresent map[string]bool // stage file name to whether it existed when Run was called
	stagedSourcesList string          // the staged sources.list as Run found it; the work directory is gone afterwards
}

func (f *fakeBootstrapper) Run(_ context.Context, spec BootstrapSpec) error {
	f.runCount++
	f.lastSpec = spec
	f.stageFilesPresent = map[string]bool{}
	for _, stageFileName := range []string{"wsl.conf", "sudoers", "provision.sh", "sources.list", "auto-marks"} {
		_, err := os.Stat(filepath.Join(spec.WorkDir, "stage", stageFileName))
		f.stageFilesPresent[stageFileName] = err == nil
	}
	if sourcesList, err := os.ReadFile(filepath.Join(spec.WorkDir, "stage", "sources.list")); err == nil {
		f.stagedSourcesList = string(sourcesList)
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
	if listsParent := hookArgument(spec.CustomizeHooks, "copy-out /var/lib/apt/lists "); listsParent != "" && !f.skipAptLists {
		aptLists := f.aptLists
		if aptLists == nil {
			aptLists = sampleAptLists()
		}
		if err := writeAptLists(filepath.Join(listsParent, "lists"), listsForSpec(aptLists, spec)); err != nil {
			return err
		}
	}
	if statesPath := hookArgument(spec.CustomizeHooks, "download /var/lib/apt/extended_states "); statesPath != "" && !f.skipExtendedStates {
		extendedStates := f.extendedStates
		if extendedStates == "" {
			extendedStates = sampleExtendedStates
		}
		if err := os.WriteFile(statesPath, []byte(extendedStates), 0o644); err != nil {
			return err
		}
	}
	dpkgStatus := f.dpkgStatus
	if dpkgStatus == "" {
		dpkgStatus = sampleDpkgStatus
	}
	return os.WriteFile(hookArgument(spec.CustomizeHooks, "download /var/lib/dpkg/status "), []byte(dpkgStatus), 0o644)
}

// listsForSpec renames the sample indexes, which are named for jammy on the
// archive, to the archive and suite of spec's first source line, as apt
// would name them for that build (a --mirror build, a focal build).
func listsForSpec(lists map[string]string, spec BootstrapSpec) map[string]string {
	if len(spec.SourceLines) == 0 {
		return lists
	}
	fields := strings.Fields(spec.SourceLines[0]) // deb URL suite components...
	if len(fields) < 3 || strings.HasPrefix(fields[1], "[") {
		return lists
	}
	prefix, suite := AptListPrefix(fields[1]), fields[2]
	renamed := map[string]string{}
	for name, content := range lists {
		renamed[strings.Replace(name, "archive.ubuntu.com_ubuntu_dists_jammy", prefix+"_dists_"+suite, 1)] = content
	}
	return renamed
}

// writeAptLists writes index files by name into listsDir, as copy-out would.
func writeAptLists(listsDir string, filesByName map[string]string) error {
	if err := os.MkdirAll(listsDir, 0o755); err != nil {
		return err
	}
	for name, content := range filesByName {
		if err := os.WriteFile(filepath.Join(listsDir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// hookArgument finds the single-quoted host path following prefix in one of
// the hooks, the way mmdebstrap's shellwords would read it, or "".
func hookArgument(hooks []string, prefix string) string {
	for _, hook := range hooks {
		if quotedPath, found := strings.CutPrefix(hook, prefix); found {
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
	options.Getenv = fakeEnvironment(map[string]string{"XDG_CACHE_HOME": filepath.Dir(workRoot), SourceDateEpochVariable: sampleEpoch})
	bootstrapper := &fakeBootstrapper{}
	result, err := buildWith(bootstrapper, options)
	if err != nil {
		t.Fatal(err)
	}

	spec := bootstrapper.lastSpec
	if spec.Suite != "jammy" || spec.Arch != "amd64" || !spec.InstallRecommends {
		t.Errorf("spec = %+v, want jammy, amd64 and Recommends", spec)
	}
	if spec.SourceDateEpoch != 1758067200 {
		t.Errorf("SourceDateEpoch = %d, want the environment's %s", spec.SourceDateEpoch, sampleEpoch)
	}
	if spec.KeyringPath != "/usr/share/keyrings/ubuntu-archive-keyring.gpg" {
		t.Errorf("KeyringPath = %q", spec.KeyringPath)
	}
	wantSourceLines := []string{
		"deb http://archive.ubuntu.com/ubuntu jammy main restricted universe multiverse",
		"deb http://archive.ubuntu.com/ubuntu jammy-updates main restricted universe multiverse",
		"deb http://archive.ubuntu.com/ubuntu jammy-security main restricted universe multiverse",
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
	for _, stageFileName := range []string{"wsl.conf", "sudoers", "provision.sh"} {
		if !bootstrapper.stageFilesPresent[stageFileName] {
			t.Errorf("stage file %s must exist before the bootstrap runs", stageFileName)
		}
	}
	if !bootstrapper.stageFilesPresent["sources.list"] {
		t.Error("every build uploads the image's sources.list, so the lines the image keeps are the archive's")
	}
	if len(spec.CustomizeHooks) != 9 {
		t.Errorf("CustomizeHooks = %q, want upload, upload, upload sources.list, provision, remove the host's files, copy-out, ensure and download extended_states, download status", spec.CustomizeHooks)
	}
	if spec.Trusted || spec.KeyringPath == "" {
		t.Errorf("an online build verifies the archive with a keyring: %+v", spec)
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
		SourceDateEpoch:  1758067200,
		Packages: []recipe.LockPackage{ // every installed package, sorted, with the checksums from the apt indexes and apt's auto marks
			{Name: "git", Version: "1:2.34.1-1ubuntu1.11", Arch: "amd64", SHA256: sampleGitSHA256, Size: 3165964, Filename: "pool/main/g/git/git_1%3a2.34.1-1ubuntu1.11_amd64.deb"},
			{Name: "libc6", Version: "2.35-0ubuntu3.8", Arch: "amd64", Auto: true, SHA256: sampleLibc6SHA256, Size: 3235712, Filename: "pool/main/g/glibc/libc6_2.35-0ubuntu3.8_amd64.deb"},
		},
	}
	if !slices.Equal(lock.Sources, wantLock.Sources) || !slices.Equal(lock.Requested, wantLock.Requested) ||
		!slices.Equal(lock.Packages, wantLock.Packages) {
		t.Errorf("lock =\n%+v\nwant\n%+v", lock, wantLock)
	}
	if lock.Version != wantLock.Version || lock.Distro != wantLock.Distro || lock.Release != wantLock.Release ||
		lock.Suite != wantLock.Suite || lock.Arch != wantLock.Arch || lock.Mirror != wantLock.Mirror ||
		lock.FrostrootVersion != wantLock.FrostrootVersion || lock.SourceDateEpoch != wantLock.SourceDateEpoch {
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
		SourceDateEpoch:       1758067200,
	}
	if result != wantResult {
		t.Errorf("Result = %+v, want %+v", result, wantResult)
	}
	assertNotCreated(t, spec.WorkDir) // removed after success
	assertNoTemporaryFiles(t, options.RecipeDir)
}

func TestBuildFreezesAtItsStartWithoutAnEpoch(t *testing.T) {
	options, _ := newTestOptions(t)
	bootstrapper := &fakeBootstrapper{}
	before := time.Now().Unix()
	result, err := buildWith(bootstrapper, options)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().Unix()
	if result.SourceDateEpoch < before || result.SourceDateEpoch > after {
		t.Errorf("SourceDateEpoch = %d, want the build's start between %d and %d", result.SourceDateEpoch, before, after)
	}
	if bootstrapper.lastSpec.SourceDateEpoch != result.SourceDateEpoch {
		t.Errorf("mmdebstrap got %d, want the result's %d", bootstrapper.lastSpec.SourceDateEpoch, result.SourceDateEpoch)
	}
	lock, err := recipe.LoadLock(result.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if lock.SourceDateEpoch != result.SourceDateEpoch {
		t.Errorf("the lock records %d, want %d", lock.SourceDateEpoch, result.SourceDateEpoch)
	}
	if result.Reproducible {
		t.Error("an online build promises no byte identity: its install order differs from the offline rebuild's")
	}
}

func TestBuildRefusesUnusableEpochBeforeAnyWork(t *testing.T) {
	options, workRoot := newTestOptions(t)
	options.Getenv = fakeEnvironment(map[string]string{"XDG_CACHE_HOME": filepath.Dir(workRoot), SourceDateEpochVariable: "yesterday"})
	bootstrapper := &fakeBootstrapper{}
	result, err := buildWith(bootstrapper, options)
	if !errors.Is(err, ErrBadSourceDateEpoch) {
		t.Fatalf("Build error = %v, want ErrBadSourceDateEpoch", err)
	}
	if bootstrapper.runCount != 0 || result.WorkDir != "" {
		t.Errorf("runs = %d, Result = %+v; want no bootstrap and no work directory", bootstrapper.runCount, result)
	}
	assertNotCreated(t, workRoot)
}

func TestBuildRecordsAutoMarksAsAptWroteThem(t *testing.T) {
	// apt records an "all" package under the native architecture, a mark
	// taken back (Auto-Installed: 0) is no mark, and a mark for a package no
	// longer installed is ignored.
	options, _ := newTestOptions(t)
	bootstrapper := &fakeBootstrapper{
		dpkgStatus: sampleDpkgStatus + "\nPackage: git-man\nStatus: install ok installed\nArchitecture: all\nVersion: 1:2.34.1-1ubuntu1.11\n",
		aptLists: map[string]string{
			"archive.ubuntu.com_ubuntu_dists_jammy-updates_main_binary-amd64_Packages": sampleAptLists()["archive.ubuntu.com_ubuntu_dists_jammy-updates_main_binary-amd64_Packages"] +
				"\nPackage: git-man\nArchitecture: all\nVersion: 1:2.34.1-1ubuntu1.11\nFilename: pool/main/g/git/git-man_1%3a2.34.1-1ubuntu1.11_all.deb\nSize: 950000\nSHA256: cc\n",
			"archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages": sampleAptLists()["archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages"],
		},
		extendedStates: "Package: git-man\nArchitecture: amd64\nAuto-Installed: 1\n\n" +
			"Package: libc6\nArchitecture: amd64\nAuto-Installed: 0\n\n" +
			"Package: removed-since\nArchitecture: amd64\nAuto-Installed: 1\n\n",
	}
	if _, err := buildWith(bootstrapper, options); err != nil {
		t.Fatal(err)
	}
	lock, err := recipe.LoadLock(filepath.Join(options.RecipeDir, LockFileName))
	if err != nil {
		t.Fatal(err)
	}
	autoByName := map[string]bool{}
	for _, locked := range lock.Packages {
		autoByName[locked.Name] = locked.Auto
	}
	if wantAuto := map[string]bool{"git": false, "git-man": true, "libc6": false}; !maps.Equal(autoByName, wantAuto) {
		t.Errorf("auto marks = %v, want %v", autoByName, wantAuto)
	}
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
		{
			// A lock without checksums is one vendor cannot act on.
			name:         "apt lists missing",
			bootstrapper: &fakeBootstrapper{skipAptLists: true},
			wantInError:  "apt indexes",
		},
		{
			// The hooks make sure the file exists, so its absence means a
			// hook did not run.
			name:         "apt extended_states missing",
			bootstrapper: &fakeBootstrapper{skipExtendedStates: true},
			wantInError:  "extended_states",
		},
		{
			name:         "installed package not in any index",
			bootstrapper: &fakeBootstrapper{dpkgStatus: sampleDpkgStatus + "\nPackage: mystery\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1\n"},
			wantInError:  "mystery 1 amd64",
		},
		{
			name: "indexes disagree about a checksum",
			bootstrapper: &fakeBootstrapper{aptLists: map[string]string{
				"archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages":          "Package: libc6\nArchitecture: amd64\nVersion: 2.35-0ubuntu3.8\nFilename: pool/main/g/glibc/a.deb\nSize: 1\nSHA256: aa\n\nPackage: git\nArchitecture: amd64\nVersion: 1:2.34.1-1ubuntu1.11\nFilename: pool/main/g/git/g.deb\nSize: 2\nSHA256: bb\n",
				"archive.ubuntu.com_ubuntu_dists_jammy-security_main_binary-amd64_Packages": "Package: libc6\nArchitecture: amd64\nVersion: 2.35-0ubuntu3.8\nFilename: pool/main/g/glibc/a.deb\nSize: 1\nSHA256: cc\n",
			}},
			wantInError: "disagree about libc6",
		},
		{
			// An index from nowhere would give a package a checksum with no
			// known origin.
			name: "index from an unknown source",
			bootstrapper: &fakeBootstrapper{aptLists: map[string]string{
				"archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages": sampleAptLists()["archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages"],
				"evil.example_repo_dists_jammy_main_binary-amd64_Packages":         "Package: git\nArchitecture: amd64\nVersion: 1:2.34.1-1ubuntu1.11\nFilename: pool/g.deb\nSize: 2\nSHA256: bb\n",
			}},
			wantInError: "evil.example_repo_dists_jammy_main_binary-amd64_Packages belongs to no source",
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
			// The recipe here has no extra sources, which is what used to
			// skip this check: an offline build still hands apt a
			// "copy://<work root>/pool" line, and apt splits it on the space.
			name: "work root with a space",
			adjust: func(options *Options) {
				options.Getenv = fakeEnvironment(map[string]string{"XDG_CACHE_HOME": filepath.Join(t.TempDir(), "my cache")})
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

func TestBuildRecordsFileNameFromTheNewestPocket(t *testing.T) {
	// A package moved between components after release is listed at two
	// pool paths with one checksum; the -updates or -security index has the
	// path the file currently lives at.
	options, _ := newTestOptions(t)
	bootstrapper := &fakeBootstrapper{
		dpkgStatus: "Package: ocl-icd-dev\nStatus: install ok installed\nArchitecture: amd64\nVersion: 2.3.2-1build1\n",
		aptLists: map[string]string{
			"archive.ubuntu.com_ubuntu_dists_jammy-updates_main_binary-amd64_Packages": "Package: ocl-icd-dev\nArchitecture: amd64\nVersion: 2.3.2-1build1\nFilename: pool/main/o/ocl-icd/ocl-icd-dev_2.3.2-1build1_amd64.deb\nSize: 10118\nSHA256: 66e9\n",
			"archive.ubuntu.com_ubuntu_dists_jammy_universe_binary-amd64_Packages":     "Package: ocl-icd-dev\nArchitecture: amd64\nVersion: 2.3.2-1build1\nFilename: pool/universe/o/ocl-icd/ocl-icd-dev_2.3.2-1build1_amd64.deb\nSize: 10118\nSHA256: 66E9\n",
		},
	}
	if _, err := buildWith(bootstrapper, options); err != nil {
		t.Fatal(err)
	}
	lock, err := recipe.LoadLock(filepath.Join(options.RecipeDir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Packages) != 1 || lock.Packages[0].Filename != "pool/main/o/ocl-icd/ocl-icd-dev_2.3.2-1build1_amd64.deb" || lock.Packages[0].SHA256 != "66e9" {
		t.Errorf("lock packages = %+v, want the -updates path and a lowercase digest", lock.Packages)
	}
}

// writeVendoredLock builds sampleRecipe online into options.RecipeDir and
// then fills vendor/debs with real .deb files matching the lock's names,
// rewriting the lock's checksums to those files. It returns the lock.
func writeVendoredLock(t *testing.T, options Options) recipe.Lockfile {
	t.Helper()
	if _, err := buildWith(&fakeBootstrapper{}, options); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(options.RecipeDir, LockFileName)
	lock, err := recipe.LoadLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	poolDir := filepath.Join(options.RecipeDir, "vendor", "debs")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for index, locked := range lock.Packages {
		fileName := filepath.Base(locked.Filename)
		path := debtest.Build(t, poolDir, fileName, ".zst", debtest.Control(locked.Name, locked.Version, locked.Arch))
		size, digest, err := deb.SHA256File(path)
		if err != nil {
			t.Fatal(err)
		}
		lock.Packages[index].Size, lock.Packages[index].SHA256 = size, digest
	}
	if err := recipe.SaveLock(lockPath, lock); err != nil {
		t.Fatal(err)
	}
	// Remove the online build's tarball so the offline one is told apart.
	if err := os.Remove(expectedTarballPath(options)); err != nil {
		t.Fatal(err)
	}
	return lock
}

// offlineFakeBootstrapper writes a tarball and a dpkg status file, reads no
// apt lists (there are none offline), and records the local repository it
// was given.
type offlineFakeBootstrapper struct {
	fakeBootstrapper
	repositoryFiles []string // names in the copy:// repository when Run was called
}

func (f *offlineFakeBootstrapper) Run(ctx context.Context, spec BootstrapSpec) error {
	if len(spec.SourceLines) == 1 {
		repositoryDir := strings.TrimSuffix(strings.TrimPrefix(spec.SourceLines[0], "deb [trusted=yes] copy://"), " ./")
		entries, err := os.ReadDir(repositoryDir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			f.repositoryFiles = append(f.repositoryFiles, entry.Name())
		}
	}
	return f.fakeBootstrapper.Run(ctx, spec)
}

func TestBuildOfflineRebuildsFromThePool(t *testing.T) {
	options, workRoot := newTestOptions(t)
	lock := writeVendoredLock(t, options)
	lockPath := filepath.Join(options.RecipeDir, LockFileName)
	lockBefore, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	options.Offline = true
	// The lock's instant is the input; the environment's is ignored.
	options.Getenv = fakeEnvironment(map[string]string{"XDG_CACHE_HOME": filepath.Dir(workRoot), SourceDateEpochVariable: "1700000000"})
	recorder := &recordingProgress{}
	options.Progress = recorder
	bootstrapper := &offlineFakeBootstrapper{}
	result, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), sampleRecipe(), options)
	if err != nil {
		t.Fatal(err)
	}
	spec := bootstrapper.lastSpec
	if !spec.Trusted || spec.KeyringPath != "" {
		t.Errorf("an offline build trusts its own verified repository, no keyring: %+v", spec)
	}
	if lock.SourceDateEpoch <= 0 || spec.SourceDateEpoch != lock.SourceDateEpoch {
		t.Errorf("SourceDateEpoch = %d, want the lock's %d, whatever the environment says", spec.SourceDateEpoch, lock.SourceDateEpoch)
	}
	if !result.Reproducible || result.SourceDateEpoch != lock.SourceDateEpoch {
		t.Errorf("Result = %+v, want a reproducible build frozen at the lock's instant", result)
	}
	if !bootstrapper.stageFilesPresent["auto-marks"] {
		t.Error("an offline build must stage the lock's auto marks for the image")
	}
	autoMarks, err := os.ReadFile(filepath.Join(spec.WorkDir, "stage", "auto-marks"))
	if err == nil && string(autoMarks) != sampleExtendedStates {
		t.Errorf("staged auto marks = %q, want the lock's marks in apt's format", autoMarks)
	}
	hookCount := len(spec.CustomizeHooks)
	if hookCount < 2 || spec.CustomizeHooks[hookCount-2] != "upload '"+filepath.Join(spec.WorkDir, "stage", "auto-marks")+"' /var/lib/apt/extended_states" {
		t.Errorf("hooks = %q, want the auto marks uploaded right before the status download", spec.CustomizeHooks)
	}
	if strings.Contains(strings.Join(spec.CustomizeHooks, "\n"), "download /var/lib/apt/extended_states") {
		t.Errorf("hooks = %q, want no download of apt's marks offline", spec.CustomizeHooks)
	}
	if len(spec.SourceLines) != 1 || !strings.HasPrefix(spec.SourceLines[0], "deb [trusted=yes] copy://"+spec.WorkDir+"/pool ./") {
		t.Errorf("SourceLines = %q, want the local repository only", spec.SourceLines)
	}
	if wantInclude := []string{"git", "libc6"}; !slices.Equal(spec.Include, wantInclude) {
		t.Errorf("Include = %q, want every locked package, sorted", spec.Include)
	}
	if !bootstrapper.stageFilesPresent["sources.list"] {
		t.Error("an offline build must stage a sources.list from the lock for the image")
	}
	if !slices.Contains(spec.CustomizeHooks, "upload '"+filepath.Join(spec.WorkDir, "stage", "sources.list")+"' /etc/apt/sources.list") {
		t.Errorf("hooks = %q, want the sources.list upload", spec.CustomizeHooks)
	}
	if strings.Contains(strings.Join(spec.CustomizeHooks, "\n"), "copy-out") {
		t.Errorf("hooks = %q, want no apt lists copied offline", spec.CustomizeHooks)
	}
	if bootstrapper.stagedSourcesList != strings.Join(lock.Sources, "\n")+"\n" {
		t.Errorf("staged sources.list = %q, want the lock's sources", bootstrapper.stagedSourcesList)
	}
	slices.Sort(bootstrapper.repositoryFiles)
	wantFiles := []string{"Packages", "Release", "git_1%3a2.34.1-1ubuntu1.11_amd64.deb", "libc6_2.35-0ubuntu3.8_amd64.deb"}
	if !slices.Equal(bootstrapper.repositoryFiles, wantFiles) {
		t.Errorf("repository held %q, want %q", bootstrapper.repositoryFiles, wantFiles)
	}

	lockAfter, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(lockAfter) != string(lockBefore) {
		t.Error("an offline build must not rewrite the lock")
	}
	if !result.Offline || result.LockPath != lockPath || result.InstalledPackageCount != 2 {
		t.Errorf("Result = %+v", result)
	}
	if _, err := os.Stat(expectedTarballPath(options)); err != nil {
		t.Errorf("tarball not placed: %v", err)
	}
	var started []Phase
	for _, event := range recorder.ofKind(EventPhaseStarted) {
		started = append(started, event.Phase)
	}
	if wantStarted := []Phase{PhaseVerifyVendored, PhasePrepareRepository, PhaseCheckLock, PhasePlaceTarball}; !slices.Equal(started, wantStarted) {
		t.Errorf("phases started = %v, want %v", started, wantStarted)
	}
	assertNoTemporaryFiles(t, options.RecipeDir)
}

// A lock made before v0.10 names main and universe only. Its offline rebuild
// must write those lines into the image, not the four components a build
// enables today, or the tarball would stop matching the one it reproduces.
func TestBuildOfflineKeepsTheComponentsOfAnOlderLock(t *testing.T) {
	options, _ := newTestOptions(t)
	lock := writeVendoredLock(t, options)
	lockPath := filepath.Join(options.RecipeDir, LockFileName)
	lock.Sources = []string{
		"deb http://archive.ubuntu.com/ubuntu jammy main universe",
		"deb http://archive.ubuntu.com/ubuntu jammy-updates main universe",
		"deb http://archive.ubuntu.com/ubuntu jammy-security main universe",
	}
	if err := recipe.SaveLock(lockPath, lock); err != nil {
		t.Fatal(err)
	}

	options.Offline = true
	bootstrapper := &offlineFakeBootstrapper{}
	if _, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), sampleRecipe(), options); err != nil {
		t.Fatalf("an older lock must still rebuild offline: %v", err)
	}
	if want := strings.Join(lock.Sources, "\n") + "\n"; bootstrapper.stagedSourcesList != want {
		t.Errorf("staged sources.list = %q, want the older lock's own lines %q", bootstrapper.stagedSourcesList, want)
	}
}

func TestBuildOfflineOldLockFreezesAtNowWithoutPromise(t *testing.T) {
	// A lock from frostroot 0.4 or 0.5 records no instant and no auto marks.
	// It still builds, frozen at the build's start like an online build, and
	// the result says the tarball matches no other.
	options, _ := newTestOptions(t)
	lock := writeVendoredLock(t, options)
	lock.FrostrootVersion = "0.5.0"
	lock.SourceDateEpoch = 0
	for index := range lock.Packages {
		lock.Packages[index].Auto = false
	}
	if err := recipe.SaveLock(filepath.Join(options.RecipeDir, LockFileName), lock); err != nil {
		t.Fatal(err)
	}
	options.Offline = true
	bootstrapper := &offlineFakeBootstrapper{}
	before := time.Now().Unix()
	result, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), sampleRecipe(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reproducible {
		t.Error("an old lock cannot promise byte identity")
	}
	if spec := bootstrapper.lastSpec; spec.SourceDateEpoch < before || spec.SourceDateEpoch > time.Now().Unix() || result.SourceDateEpoch != spec.SourceDateEpoch {
		t.Errorf("SourceDateEpoch = %d (result %d), want the build's start", spec.SourceDateEpoch, result.SourceDateEpoch)
	}
	if bootstrapper.stageFilesPresent["auto-marks"] || strings.Contains(strings.Join(bootstrapper.lastSpec.CustomizeHooks, "\n"), "extended_states") {
		t.Errorf("with no marks in the lock nothing is staged or uploaded: %q", bootstrapper.lastSpec.CustomizeHooks)
	}
}

func TestBuildOfflineFailsWhenTheImageDiffersFromTheLock(t *testing.T) {
	options, _ := newTestOptions(t)
	writeVendoredLock(t, options)
	options.Offline = true
	bootstrapper := &offlineFakeBootstrapper{fakeBootstrapper: fakeBootstrapper{
		dpkgStatus: "Package: libc6\nStatus: install ok installed\nArchitecture: amd64\nVersion: 2.35-0ubuntu3.9\n\nPackage: git\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1:2.34.1-1ubuntu1.11\n",
	}}
	result, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), sampleRecipe(), options)
	if !errors.Is(err, ErrImageDiffersFromLock) {
		t.Fatalf("Build error = %v, want ErrImageDiffersFromLock", err)
	}
	for _, wantText := range []string{"libc6 2.35-0ubuntu3.8 amd64", "libc6 2.35-0ubuntu3.9 amd64"} {
		if !strings.Contains(err.Error(), wantText) {
			t.Errorf("error = %v, want it to name %q", err, wantText)
		}
	}
	if result.WorkDir == "" {
		t.Error("a failed offline build keeps its work directory")
	}
	if _, err := os.Stat(expectedTarballPath(options)); !errors.Is(err, os.ErrNotExist) {
		t.Error("no tarball may be placed when the image differs from the lock")
	}
}

func TestBuildOfflineRefusesBeforeAnyWork(t *testing.T) {
	testCases := []struct {
		name      string
		prepare   func(t *testing.T, options Options) recipe.Recipe
		wantError error
		wantText  string
	}{
		{
			name:      "no lock",
			prepare:   func(*testing.T, Options) recipe.Recipe { return sampleRecipe() },
			wantError: ErrNoLock,
			wantText:  "frostroot build online first",
		},
		{
			name: "lock without checksums",
			prepare: func(t *testing.T, options Options) recipe.Recipe {
				t.Helper()
				lock := writeVendoredLock(t, options)
				for index := range lock.Packages {
					lock.Packages[index].SHA256, lock.Packages[index].Size, lock.Packages[index].Filename = "", 0, ""
				}
				lock.FrostrootVersion = "0.3.0"
				if err := recipe.SaveLock(filepath.Join(options.RecipeDir, LockFileName), lock); err != nil {
					t.Fatal(err)
				}
				return sampleRecipe()
			},
			wantError: pool.ErrNoChecksums,
			wantText:  "0.3.0",
		},
		{
			name: "recipe release changed",
			prepare: func(t *testing.T, options Options) recipe.Recipe {
				t.Helper()
				writeVendoredLock(t, options)
				imageRecipe := sampleRecipe()
				imageRecipe.Image.Release = "24.04"
				return imageRecipe
			},
			wantError: ErrLockMismatch,
			wantText:  "release 22.04 in the lock, 24.04 in the recipe",
		},
		{
			name: "recipe packages changed",
			prepare: func(t *testing.T, options Options) recipe.Recipe {
				t.Helper()
				writeVendoredLock(t, options)
				imageRecipe := sampleRecipe()
				imageRecipe.Packages.Include = []string{"cmake", "git", "ninja-build"} // build-essential removed, ninja-build added, order changed
				return imageRecipe
			},
			wantError: ErrLockMismatch,
			wantText:  "added to the recipe: ninja-build; packages removed from the recipe: build-essential",
		},
		{
			name: "pool incomplete",
			prepare: func(t *testing.T, options Options) recipe.Recipe {
				t.Helper()
				lock := writeVendoredLock(t, options)
				if err := os.Remove(filepath.Join(options.RecipeDir, "vendor", "debs", filepath.Base(lock.Packages[0].Filename))); err != nil {
					t.Fatal(err)
				}
				return sampleRecipe()
			},
			wantError: ErrPoolIncomplete,
			wantText:  "git_1%3a2.34.1-1ubuntu1.11_amd64.deb (missing); run frostroot vendor",
		},
		{
			name: "pool corrupt",
			prepare: func(t *testing.T, options Options) recipe.Recipe {
				t.Helper()
				lock := writeVendoredLock(t, options)
				if err := os.WriteFile(filepath.Join(options.RecipeDir, "vendor", "debs", filepath.Base(lock.Packages[1].Filename)), []byte("tampered"), 0o644); err != nil {
					t.Fatal(err)
				}
				return sampleRecipe()
			},
			wantError: ErrPoolIncomplete,
			wantText:  "libc6_2.35-0ubuntu3.8_amd64.deb (wrong size or checksum)",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			options, _ := newTestOptions(t)
			imageRecipe := testCase.prepare(t, options)
			options.Offline = true
			bootstrapper := &offlineFakeBootstrapper{}
			result, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), imageRecipe, options)
			if !errors.Is(err, testCase.wantError) {
				t.Fatalf("Build error = %v, want %v", err, testCase.wantError)
			}
			if !strings.Contains(err.Error(), testCase.wantText) {
				t.Errorf("error = %v, want it to say %q", err, testCase.wantText)
			}
			if bootstrapper.runCount != 0 || result.WorkDir != "" {
				t.Errorf("runs = %d, Result = %+v; want no bootstrap and no work directory", bootstrapper.runCount, result)
			}
		})
	}
}

func TestBuildOfflineRejectsMirror(t *testing.T) {
	options, _ := newTestOptions(t)
	options.Offline = true
	options.MirrorURL = "http://mirror.example/ubuntu"
	if _, err := buildWith(&fakeBootstrapper{}, options); err == nil || !strings.Contains(err.Error(), "mirror") {
		t.Fatalf("Build error = %v, want a refusal naming the mirror", err)
	}
}

func TestBuildOfflineUserChangesAreAllowed(t *testing.T) {
	// The user, sudo, locale, timezone and systemd are provisioned at build
	// time and need no packages, so they may differ from the lock's build.
	options, _ := newTestOptions(t)
	writeVendoredLock(t, options)
	options.Offline = true
	imageRecipe := sampleRecipe()
	imageRecipe.User = recipe.User{Name: "teacher", Sudo: false}
	imageRecipe.WSL = recipe.WSL{Systemd: false}
	imageRecipe.Locale = recipe.Locale{Lang: "tr_TR.UTF-8", Timezone: "UTC"}
	imageRecipe.Packages.Include = []string{"cmake", "build-essential", "git"} // same set, other order
	if _, err := (&Builder{Bootstrapper: &offlineFakeBootstrapper{}}).Build(context.Background(), imageRecipe, options); err != nil {
		t.Fatal(err)
	}
}

func TestCompareWithLock(t *testing.T) {
	lock := recipe.Lockfile{Packages: []recipe.LockPackage{{Name: "a", Version: "1", Arch: "amd64"}, {Name: "b", Version: "2", Arch: "all"}}}
	if err := compareWithLock(lock, []recipe.LockPackage{{Name: "b", Version: "2", Arch: "all"}, {Name: "a", Version: "1", Arch: "amd64"}}); err != nil {
		t.Errorf("the same set in another order is equal: %v", err)
	}
	err := compareWithLock(lock, []recipe.LockPackage{{Name: "a", Version: "1", Arch: "amd64"}, {Name: "c", Version: "3", Arch: "amd64"}})
	if !errors.Is(err, ErrImageDiffersFromLock) || !strings.Contains(err.Error(), "not in the image: b 2 all") || !strings.Contains(err.Error(), "not in the lock: c 3 amd64") {
		t.Errorf("error = %v", err)
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

// TestBuildLeavesNoTarballWhenTheLockCannotBePlaced: Build promises that a
// failed build writes no lock and no tarball. The tarball is placed first,
// so a lock that cannot be renamed into place used to leave dist/ holding a
// new image beside an older lock, with nothing saying the two disagree; an
// offline rebuild or a vendor run would then work from the wrong lock.
// A directory in the lock's place is what makes the rename fail here.
func TestBuildLeavesNoTarballWhenTheLockCannotBePlaced(t *testing.T) {
	options, _ := newTestOptions(t)
	lockPath := filepath.Join(options.RecipeDir, LockFileName)
	if err := os.MkdirAll(filepath.Join(lockPath, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := buildWith(&fakeBootstrapper{}, options)
	if err == nil {
		t.Fatal("a build whose lock cannot be placed must fail")
	}
	if !strings.Contains(err.Error(), "placing lock") {
		t.Errorf("error = %v, want it to name the lock", err)
	}
	if _, statErr := os.Stat(expectedTarballPath(options)); !os.IsNotExist(statErr) {
		t.Errorf("dist/ still holds a tarball the lock does not describe: %v", statErr)
	}
	if result.WorkDir == "" {
		t.Error("a failed build keeps its work directory")
	}
	assertNoTemporaryFiles(t, options.RecipeDir)
}
