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

	"frostroot/internal/distro"
	"frostroot/internal/export"
	"frostroot/internal/recipe"
)

// Version is the frostroot version recorded in every lockfile.
const Version = "0.2.0"

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
	SourceLines       []string // the three "deb URL suite components" lines
	Include           []string // packages to install on top of the base system
	CustomizeHooks    []string // mmdebstrap --customize-hook arguments, in order
	TarballPath       string   // where the bootstrapper writes the image
	WorkDir           string   // scratch space; during Preflight, the work root instead
	Arch              string   // CPU architecture, such as "amd64"
	InstallRecommends bool     // install Recommends, as apt does by default
	KeyringPath       string   // keyring that verifies the archive
	Progress          Progress // receives phases and output lines; nil discards them
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
}

// Result describes a finished or failed build.
type Result struct {
	LockPath              string
	TarballPath           string
	InstalledPackageCount int    // packages recorded in the lock
	WorkDir               string // set when the work directory was kept: KeepWork, a failure, or a failed cleanup
	CleanupErr            error  // the build succeeded but the work directory could not be removed
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
	currentUID := os.Getuid()
	workRoot, err := WorkRoot(getenv, currentUID)
	if err != nil {
		return Result{}, err
	}
	progress := progressOrDiscard(options.Progress)
	bootstrapSpec := BootstrapSpec{
		Suite:             release.Suite,
		SourceLines:       release.SourceLines(options.MirrorURL),
		Include:           PackagesToInstall(imageRecipe.Packages.Include),
		Arch:              imageRecipe.Image.Arch,
		InstallRecommends: true,
		KeyringPath:       UbuntuArchiveKeyring,
		WorkDir:           workRoot,
		Progress:          progress,
	}
	if preflighter, ok := b.Bootstrapper.(Preflighter); ok {
		if err := preflighter.Preflight(bootstrapSpec); err != nil {
			return Result{}, err
		}
	}
	if err := checkOutputWritable(options.RecipeDir); err != nil {
		return Result{}, err
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
	result := Result{WorkDir: workDir}
	var temporaryLockPath string
	failBuild := func(err error) (Result, error) {
		if temporaryLockPath != "" {
			// Best effort: the build has already failed, and that error is the
			// one returned.
			_ = os.Remove(temporaryLockPath)
		}
		return result, err
	}

	stage, err := WriteStage(filepath.Join(workDir, "stage"), imageRecipe)
	if err != nil {
		return failBuild(err)
	}
	bootstrapSpec.CustomizeHooks = CustomizeHooks(stage)
	bootstrapSpec.TarballPath = filepath.Join(workDir, "image.tar.gz")
	bootstrapSpec.WorkDir = workDir
	if err := b.Bootstrapper.Run(ctx, bootstrapSpec); err != nil {
		return failBuild(err)
	}

	progress.Report(ProgressEvent{Phase: PhaseWriteLock, Kind: EventPhaseStarted})
	installedPackages, err := readDpkgStatus(stage.DpkgStatusPath)
	if err != nil {
		return failBuild(err)
	}
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
		Sources:          bootstrapSpec.SourceLines,
		FrostrootVersion: Version,
		Requested:        requestedPackages,
		Packages:         installedPackages,
	}
	temporaryLockPath, err = writeTemporaryLock(options.RecipeDir, lock)
	if err != nil {
		return failBuild(err)
	}
	progress.Report(ProgressEvent{Phase: PhaseWriteLock, Kind: EventPhaseFinished})

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
	// The lock goes into place only after the tarball has landed, so a lock
	// never describes an image that does not exist.
	lockPath := filepath.Join(options.RecipeDir, "frostroot.lock")
	if err := os.Rename(temporaryLockPath, lockPath); err != nil {
		return failBuild(fmt.Errorf("placing lock: %w", err))
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
