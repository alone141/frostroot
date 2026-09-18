// Package builder turns a recipe into a lockfile and an image tarball. The
// bootstrap itself sits behind the Bootstrapper interface: the real one runs
// mmdebstrap, and tests use fakes that produce the same files.
package builder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"frostroot/internal/deb"
	"frostroot/internal/distro"
	"frostroot/internal/export"
	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// Version is the frostroot version recorded in every lockfile.
const Version = "0.7.0"

// UbuntuArchiveKeyring is the keyring that verifies the Ubuntu archive's
// Release files.
const UbuntuArchiveKeyring = "/usr/share/keyrings/ubuntu-archive-keyring.gpg"

// Errors for host and configuration problems a user can fix. Every other build
// error is a failure of the build itself. Compare with errors.Is.
var (
	// ErrNotLinux means the build was started on another operating system.
	ErrNotLinux = errors.New("frostroot build requires Linux")
	// ErrNoMmdebstrap means mmdebstrap is not installed.
	ErrNoMmdebstrap = errors.New("mmdebstrap not found on PATH")
	// ErrNoKeyring means the Ubuntu archive keyring is not installed.
	ErrNoKeyring = errors.New("keyring for the Ubuntu archive not found")
	// ErrBadWorkRoot means the directory builds work in cannot be used.
	ErrBadWorkRoot = errors.New("unusable work directory")
	// ErrUnwritableOutput means the lock or the tarball could not be written.
	ErrUnwritableOutput = errors.New("cannot write the build output")
)

// BootstrapSpec is everything a Bootstrapper needs to build one image.
type BootstrapSpec struct {
	Suite             string   // apt code name, such as "noble"
	SourceLines       []string // "deb ..." lines: the three pockets, or offline the one local repository
	Include           []string // packages to install on top of the base system
	CustomizeHooks    []string // mmdebstrap --customize-hook arguments, in order
	TarballPath       string   // where the bootstrapper writes the image
	WorkDir           string   // scratch space; during Preflight, the work root instead
	Arch              string   // CPU architecture, such as "amd64"
	InstallRecommends bool     // install Recommends, as apt does by default
	KeyringPath       string   // keyring that verifies the archive; unused when Trusted
	// Trusted means the source lines carry [trusted=yes] and no keyring is
	// involved: an offline build's local repository, whose files frostroot
	// verified against the lock itself.
	Trusted bool
	// SourceDateEpoch is the instant, in seconds since 1970, that every
	// timestamp in the image is clamped to, passed to mmdebstrap as
	// SOURCE_DATE_EPOCH; 0 leaves its environment alone.
	SourceDateEpoch int64
	Progress        Progress // receives phases and output lines; nil discards them
}

// Bootstrapper builds an image tarball. Implementations write the tarball to
// spec.TarballPath and run spec.CustomizeHooks, which download the image's dpkg
// status file into the work directory.
type Bootstrapper interface {
	Run(ctx context.Context, spec BootstrapSpec) error
}

// Preflighter is implemented by bootstrappers that can check host
// requirements, such as tools and keyrings, before any work directory exists.
type Preflighter interface {
	Preflight(spec BootstrapSpec) error
}

// Options are the settings of one build.
type Options struct {
	RecipeDir string              // directory holding frostroot.toml; the lock and dist/ go here
	MirrorURL string              // replaces the archive URL in all three pockets when set
	KeepWork  bool                // keep the work directory after a successful build
	GOOS      string              // operating system; defaults to runtime.GOOS
	Getenv    func(string) string // environment lookup; defaults to os.Getenv
	Progress  Progress            // receives the build's phases and output; nil discards them
	// Offline rebuilds the lock's exact package set from vendor/debs, with no
	// archive, no network and no keyring, and leaves the lock unchanged.
	Offline bool
}

// Result describes a finished or failed build.
type Result struct {
	LockPath              string // the lock written, or offline the lock rebuilt from
	TarballPath           string
	InstalledPackageCount int    // packages recorded in the lock
	WorkDir               string // set when the work directory was kept: KeepWork, a failure, or a failed cleanup
	CleanupErr            error  // the build succeeded but the work directory could not be removed
	Offline               bool   // the image was rebuilt from the lock, which was checked and left as it was
	// PythonPackageCount is how many packages the image's virtual
	// environment holds, and 0 when the recipe asked for none.
	PythonPackageCount int
	// SourceDateEpoch is the instant the image is frozen at, in seconds
	// since 1970: no file in the tarball is dated later. Online it is
	// recorded in the lock; offline it is the lock's.
	SourceDateEpoch int64
	// Reproducible means the tarball is byte-identical with any other
	// offline build of this lock, because the instant came from the lock.
	// An online build is never reproducible in this sense (it installs in
	// another order than the offline rebuild), and neither is an offline
	// build of a lock from before frostroot 0.6, which records no instant.
	Reproducible bool
}

// Builder runs builds with a Bootstrapper.
type Builder struct {
	Bootstrapper Bootstrapper
}

// Build builds imageRecipe, which the caller has already checked with
// recipe.Validate. It resolves the release, bootstraps the image and, only if
// everything succeeded, places the tarball and then the lock. A failed build
// writes no lock, no tarball and no temporary file, and keeps its work
// directory for debugging.
//
// With Options.Offline the image is rebuilt from frostroot.lock and
// vendor/debs instead: the lock must still describe the recipe, the pool must
// hold every locked file intact, mmdebstrap installs from a local repository
// of exactly those files, and the result must list exactly the lock's
// packages. The lock is read, never written.
func (b *Builder) Build(ctx context.Context, imageRecipe recipe.Recipe, options Options) (Result, error) {
	operatingSystem := options.GOOS
	if operatingSystem == "" {
		operatingSystem = runtime.GOOS
	}
	if operatingSystem != "linux" {
		return Result{}, ErrNotLinux
	}
	getenv := options.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	release, err := distro.Lookup(imageRecipe.Image.Release, imageRecipe.Image.Arch)
	if err != nil {
		return Result{}, err
	}
	archiveURL := release.ArchiveURL
	if options.MirrorURL != "" {
		archiveURL = options.MirrorURL
	}
	var offline *offlinePlan
	if options.Offline {
		if options.MirrorURL != "" {
			return Result{}, errors.New("an offline build takes no mirror: it installs from vendor/debs")
		}
		if offline, err = planOffline(options.RecipeDir, imageRecipe, release); err != nil {
			return Result{}, err
		}
	}
	// The keys a build will trust are read before anything else is done.
	sourceKeys, err := readSourceKeys(options.RecipeDir, imageRecipe.Sources)
	if err != nil {
		return Result{}, err
	}
	instant, err := chooseFrozenInstant(getenv, offline, time.Now())
	if err != nil {
		return Result{}, err
	}
	currentUID := os.Getuid()
	workRoot, err := WorkRoot(getenv, currentUID)
	if err != nil {
		return Result{}, err
	}
	if len(imageRecipe.Sources) > 0 && strings.ContainsAny(workRoot, " \t[]") {
		return Result{}, fmt.Errorf("%w: %s contains a space or a bracket, which an apt signed-by path cannot; set XDG_CACHE_HOME to another directory", ErrBadWorkRoot, workRoot)
	}
	progress := progressOrDiscard(options.Progress)
	// The lines the image keeps: keys under /etc/apt/keyrings. The lines
	// mmdebstrap gets name the staged keys instead, once the stage exists.
	imageSourceLines := SourceLines(release, options.MirrorURL, imageRecipe.Sources, ImageKeyringDir)
	bootstrapSpec := BootstrapSpec{
		Suite:             release.Suite,
		SourceLines:       imageSourceLines,
		Include:           PackagesToInstall(imageRecipe),
		Arch:              imageRecipe.Image.Arch,
		InstallRecommends: true,
		KeyringPath:       UbuntuArchiveKeyring,
		WorkDir:           workRoot,
		SourceDateEpoch:   instant.epoch,
		Progress:          progress,
	}
	if offline != nil {
		// Every locked package, so that the outcome does not depend on the
		// priorities in the local index; see the v0.4 spec.
		bootstrapSpec.Include = offline.packageNames()
		bootstrapSpec.Trusted = true
		bootstrapSpec.KeyringPath = ""
	}
	if preflighter, ok := b.Bootstrapper.(Preflighter); ok {
		if err := preflighter.Preflight(bootstrapSpec); err != nil {
			return Result{}, err
		}
	}
	if err := checkOutputWritable(options.RecipeDir); err != nil {
		return Result{}, err
	}
	if offline != nil {
		if err := verifyPool(offline, progress); err != nil {
			return Result{}, err
		}
	}
	if err := prepareWorkRoot(workRoot, currentUID); err != nil {
		return Result{}, err
	}
	workDir, err := createBuildDir(workRoot)
	if err != nil {
		return Result{}, err
	}

	// From here on every failure keeps the work directory: it is the only
	// debugging evidence. Nothing in dist/ or the lock is touched until the
	// bootstrap has fully succeeded.
	result := Result{WorkDir: workDir, Offline: offline != nil, SourceDateEpoch: instant.epoch, Reproducible: instant.fromLock}
	var temporaryLockPath string
	failBuild := func(err error) (Result, error) {
		if temporaryLockPath != "" {
			// Best effort: the build has already failed, and that error is the
			// one returned.
			_ = os.Remove(temporaryLockPath)
		}
		return result, err
	}

	stageOptions := StageOptions{
		RecordForLock: offline == nil,
		SourceLines:   imageSourceLines,
		Python:        PythonOptions{Offline: offline != nil, SourceDateEpoch: instant.epoch},
	}
	if offline != nil {
		stageOptions.SourceLines = offline.lock.Sources
		// apt marks nothing on its own offline, since every locked package
		// is asked for by name; the lock says what the online apt marked.
		stageOptions.AutoMarks = RenderExtendedStates(offline.lock.Packages, offline.lock.Arch)
		if len(offline.wheelEntries) > 0 {
			stageOptions.Requirements = RenderRequirements(offline.lock)
			stageOptions.WheelsDir = filepath.Join(workDir, PythonWheelsDirName)
			// The pip the lock records, not the one this frostroot pins: it
			// is what the pool holds, and what the online build resolved with.
			stageOptions.Python.Pip = LockedPip(offline.lock)
		}
	}
	if len(sourceKeys) > 0 {
		stageOptions.Keys = map[string][]byte{}
		for name, key := range sourceKeys {
			stageOptions.Keys[name] = key.binary
		}
	}
	stage, err := WriteStage(filepath.Join(workDir, "stage"), imageRecipe, stageOptions)
	if err != nil {
		return failBuild(err)
	}
	switch {
	case offline != nil:
		repositoryDir := filepath.Join(workDir, "pool")
		if err := stageRepository(offline, repositoryDir, progress); err != nil {
			return failBuild(err)
		}
		if stage.WheelsDir != "" {
			if err := pool.StageFiles(offline.wheelPoolDir, offline.wheelEntries, stage.WheelsDir, nil); err != nil {
				return failBuild(fmt.Errorf("staging the vendored wheels: %w", err))
			}
		}
		bootstrapSpec.SourceLines = []string{"deb [trusted=yes] copy://" + repositoryDir + " ./"}
	case len(imageRecipe.Sources) > 0:
		// apt run by mmdebstrap resolves signed-by on the host (see the v0.5
		// spec's spike), so the lines it gets name the staged keys.
		bootstrapSpec.SourceLines = SourceLines(release, options.MirrorURL, imageRecipe.Sources, stage.KeyringDir)
	}
	bootstrapSpec.CustomizeHooks = CustomizeHooks(stage)
	bootstrapSpec.TarballPath = filepath.Join(workDir, "image.tar.gz")
	bootstrapSpec.WorkDir = workDir
	if err := b.Bootstrapper.Run(ctx, bootstrapSpec); err != nil {
		return failBuild(err)
	}

	installedPackages, err := readDpkgStatus(stage.DpkgStatusPath)
	if err != nil {
		return failBuild(err)
	}
	lockPath := filepath.Join(options.RecipeDir, LockFileName)
	if offline != nil {
		progress.Report(ProgressEvent{Phase: PhaseCheckLock, Kind: EventPhaseStarted})
		if err := compareWithLock(offline.lock, installedPackages); err != nil {
			return failBuild(err)
		}
		if stage.PipListPath != "" {
			installedWheels, err := readPipList(stage.PipListPath)
			if err != nil {
				return failBuild(err)
			}
			if err := ComparePythonWithLock(offline.lock, installedWheels); err != nil {
				return failBuild(err)
			}
			result.PythonPackageCount = len(offline.lock.PyPI)
		}
		progress.Report(ProgressEvent{Phase: PhaseCheckLock, Kind: EventPhaseFinished})
	} else {
		progress.Report(ProgressEvent{Phase: PhaseWriteLock, Kind: EventPhaseStarted})
		if err := recordChecksums(stage.AptListsDir, installedPackages, indexOrigins(release, archiveURL, imageRecipe.Sources)); err != nil {
			return failBuild(err)
		}
		autoMarks, err := readExtendedStates(stage.ExtendedStatesPath)
		if err != nil {
			return failBuild(err)
		}
		markAutoInstalled(installedPackages, autoMarks, imageRecipe.Image.Arch)
		requestedPackages := imageRecipe.Packages.Include
		if requestedPackages == nil {
			requestedPackages = []string{}
		}
		lock := recipe.Lockfile{
			Version:          1,
			Distro:           "ubuntu",
			Release:          imageRecipe.Image.Release,
			Suite:            release.Suite,
			Arch:             imageRecipe.Image.Arch,
			Mirror:           archiveURL,
			Sources:          imageSourceLines,
			FrostrootVersion: Version,
			Requested:        requestedPackages,
			SourceDateEpoch:  instant.epoch,
			Repositories:     lockRepositories(release, imageRecipe.Sources, sourceKeys),
			Packages:         installedPackages,
		}
		if stage.PipReportPath != "" {
			pythonResult, err := readPipReport(stage.PipReportPath, imageRecipe.PythonPackages())
			if err != nil {
				return failBuild(err)
			}
			lock.Python = &recipe.LockPython{
				Requested:   imageRecipe.PythonPackages(),
				Venv:        PythonVenvPath,
				Interpreter: pythonResult.Interpreter,
				PipVersion:  pythonResult.PipVersion,
			}
			lock.PyPI = pythonResult.Wheels
			result.PythonPackageCount = len(pythonResult.Wheels)
		}
		temporaryLockPath, err = writeTemporaryLock(options.RecipeDir, lock)
		if err != nil {
			return failBuild(err)
		}
		progress.Report(ProgressEvent{Phase: PhaseWriteLock, Kind: EventPhaseFinished})
	}

	// mmdebstrap creates its output file before it starts, so existence alone
	// proves nothing.
	if builtTarball, err := os.Stat(bootstrapSpec.TarballPath); err != nil || builtTarball.Size() == 0 {
		return failBuild(fmt.Errorf("bootstrap reported success but left no tarball at %s", bootstrapSpec.TarballPath))
	}
	progress.Report(ProgressEvent{Phase: PhasePlaceTarball, Kind: EventPhaseStarted})
	tarballPath := filepath.Join(options.RecipeDir, export.TarballRelPath(imageRecipe.Image.Name, imageRecipe.Image.Release, imageRecipe.Image.Arch))
	reportCopied := func(copiedBytes, totalBytes int64) {
		progress.Report(ProgressEvent{Phase: PhasePlaceTarball, Kind: EventProgress, Done: copiedBytes, Total: totalBytes, Unit: UnitBytes})
	}
	if err := export.Place(bootstrapSpec.TarballPath, tarballPath, reportCopied); err != nil {
		return failBuild(fmt.Errorf("placing tarball: %w", err))
	}
	if temporaryLockPath != "" {
		// The lock goes into place only after the tarball has landed, so a
		// lock never describes an image that does not exist.
		if err := os.Rename(temporaryLockPath, lockPath); err != nil {
			return failBuild(fmt.Errorf("placing lock: %w", err))
		}
	}
	progress.Report(ProgressEvent{Phase: PhasePlaceTarball, Kind: EventPhaseFinished})

	result.LockPath = lockPath
	result.TarballPath = tarballPath
	result.InstalledPackageCount = len(installedPackages)
	if options.KeepWork {
		return result, nil
	}
	if err := os.RemoveAll(workDir); err != nil {
		result.CleanupErr = err
		return result, nil
	}
	result.WorkDir = ""
	return result, nil
}

// verifyPool checks vendor/debs against the lock, as a phase with a files
// bar, before any work directory exists.
func verifyPool(offline *offlinePlan, progress Progress) error {
	progress.Report(ProgressEvent{Phase: PhaseVerifyVendored, Kind: EventPhaseStarted})
	status, err := pool.Verify(offline.poolDir, offline.entries, func(checked, total int) {
		progress.Report(ProgressEvent{Phase: PhaseVerifyVendored, Kind: EventProgress, Done: int64(checked), Total: int64(total), Unit: UnitFiles})
	})
	if err != nil {
		return fmt.Errorf("checking %s: %w", offline.poolDir, err)
	}
	if !status.Complete() {
		return fmt.Errorf("%w: %s; run frostroot vendor", ErrPoolIncomplete, status.Describe(maxNamedPoolProblems))
	}
	if len(offline.wheelEntries) > 0 {
		wheelStatus, err := pool.Verify(offline.wheelPoolDir, offline.wheelEntries, nil)
		if err != nil {
			return fmt.Errorf("checking %s: %w", offline.wheelPoolDir, err)
		}
		if !wheelStatus.Complete() {
			return fmt.Errorf("%w: %s in %s; run frostroot vendor", ErrPoolIncomplete, wheelStatus.Describe(maxNamedPoolProblems), pool.WheelsDirName)
		}
	}
	progress.Report(ProgressEvent{Phase: PhaseVerifyVendored, Kind: EventPhaseFinished})
	return nil
}

// readPipReport reads the installation report an online build downloaded out
// of the image.
func readPipReport(path string, requested []string) (PythonResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return PythonResult{}, fmt.Errorf("bootstrap did not produce pip's installation report: %w", err)
	}
	defer func() { _ = file.Close() }() // read-only: closing cannot lose data
	return ParsePipReport(file, requested)
}

// readPipList reads the package list an offline build downloaded out of the
// image's virtual environment.
func readPipList(path string) ([]PythonInstalled, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("bootstrap did not produce the image's Python package list: %w", err)
	}
	defer func() { _ = file.Close() }() // read-only: closing cannot lose data
	return ParsePipList(file)
}

// maxNamedPoolProblems bounds how many missing or corrupt files an error
// names.
const maxNamedPoolProblems = 10

// stageRepository builds the local flat repository an offline build installs
// from, as a phase with a bytes bar.
func stageRepository(offline *offlinePlan, repositoryDir string, progress Progress) error {
	progress.Report(ProgressEvent{Phase: PhasePrepareRepository, Kind: EventPhaseStarted})
	release := deb.FlatRelease{Suite: offline.lock.Suite, Arch: offline.lock.Arch, Date: time.Now()}
	err := pool.Stage(offline.poolDir, offline.entries, repositoryDir, release, func(doneBytes, totalBytes int64) {
		progress.Report(ProgressEvent{Phase: PhasePrepareRepository, Kind: EventProgress, Done: doneBytes, Total: totalBytes, Unit: UnitBytes})
	})
	if err != nil {
		return fmt.Errorf("preparing the local repository: %w", err)
	}
	progress.Report(ProgressEvent{Phase: PhasePrepareRepository, Kind: EventPhaseFinished})
	return nil
}

// createBuildDir creates a new, uniquely named build directory under workRoot.
// Its mode is 0755 whatever the umask: in unshare mode mmdebstrap's root is
// "other" to this directory and must be able to enter it.
func createBuildDir(workRoot string) (string, error) {
	buildDir, err := os.MkdirTemp(workRoot, "build-*")
	if err != nil {
		return "", fmt.Errorf("%w: creating a build directory under %s: %w; set XDG_CACHE_HOME to use another location", ErrBadWorkRoot, workRoot, err)
	}
	if err := os.Chmod(buildDir, 0o755); err != nil {
		// Best effort: the directory is empty, and the chmod error is returned.
		_ = os.Remove(buildDir)
		return "", fmt.Errorf("%w: %w", ErrBadWorkRoot, err)
	}
	return buildDir, nil
}

// writeTemporaryLock writes lock to a uniquely named temporary file in
// directory and returns its path. A unique name means two builds in one
// directory cannot delete or overwrite each other's temporary lock.
func writeTemporaryLock(directory string, lock recipe.Lockfile) (string, error) {
	temporary, err := export.CreateTemp(directory, ".frostroot.lock.*.tmp")
	if err != nil {
		return "", fmt.Errorf("writing the lock: %w", err)
	}
	path := temporary.Name()
	err = temporary.Close()
	if err == nil {
		err = recipe.SaveLock(path, lock)
	}
	if err != nil {
		// Best effort: the write failed, and that error is the one returned.
		_ = os.Remove(path)
		return "", fmt.Errorf("writing the lock: %w", err)
	}
	return path, nil
}

// makeDirectoriesWithMode is os.MkdirAll, except that every directory it
// creates gets exactly mode instead of mode minus the umask. Existing
// directories are left alone.
func makeDirectoriesWithMode(path string, mode os.FileMode) error {
	if existing, err := os.Stat(path); err == nil {
		if !existing.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", path)
		}
		return nil
	}
	if parentPath := filepath.Dir(path); parentPath != path {
		if err := makeDirectoriesWithMode(parentPath, mode); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, mode); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil // created concurrently
		}
		return err
	}
	return os.Chmod(path, mode)
}

// readDpkgStatus parses the dpkg status file the bootstrap downloaded.
func readDpkgStatus(path string) ([]recipe.LockPackage, error) {
	statusContent, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("bootstrap did not produce the image's dpkg status: %w", err)
	}
	return ParseDpkgStatus(bytes.NewReader(statusContent))
}
