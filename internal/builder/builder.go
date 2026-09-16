// Package builder turns a recipe into a lockfile and an image tarball. The
// bootstrap itself sits behind the Bootstrapper interface: the real one runs
// mmdebstrap, tests use fakes that produce the same artifacts.
package builder

import (
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

// Version is recorded in every lockfile.
const Version = "0.1.0"

// UbuntuKeyring verifies the Ubuntu archive's Release files.
const UbuntuKeyring = "/usr/share/keyrings/ubuntu-archive-keyring.gpg"

var (
	ErrNotLinux     = errors.New("frostroot build requires Linux")
	ErrNoMmdebstrap = errors.New("mmdebstrap not found on PATH")
	ErrNoKeyring    = errors.New("Ubuntu archive keyring not found")
)

// BootstrapSpec is everything a bootstrapper needs for one image.
type BootstrapSpec struct {
	Suite      string
	Sources    []string // full "deb URL suite components" lines, all three pockets
	Include    []string
	Hooks      []string // --customize-hook arguments, in order
	TarPath    string   // the bootstrapper writes the tarball here, inside its namespace
	WorkDir    string   // scratch space for the bootstrapper (TMPDIR)
	Arch       string
	Recommends bool
	Keyring    string
}

// Bootstrapper builds an image tarball. Implementations must write the
// tarball to spec.TarPath and run spec.Hooks, which download the image's dpkg
// status file into the work directory.
type Bootstrapper interface {
	Run(ctx context.Context, spec BootstrapSpec) error
}

// Preflighter is implemented by bootstrappers that can check host
// requirements (tools, keyrings) before any work directory is created.
type Preflighter interface {
	Preflight(spec BootstrapSpec) error
}

// Options are the per-invocation settings of a build.
type Options struct {
	Dir      string // recipe directory; the lock and dist/ go here
	Mirror   string // replaces the archive base URL in all three pockets
	KeepWork bool
	GOOS     string              // default runtime.GOOS
	Getenv   func(string) string // default os.Getenv
}

// Result describes a finished or failed build.
type Result struct {
	LockPath    string
	TarballPath string
	WorkDir     string // set when the work directory was kept: KeepWork, a failure, or a failed cleanup
	CleanupErr  error  // the build succeeded but the work directory could not be removed
}

// Builder runs builds.
type Builder struct {
	Bootstrap Bootstrapper
}

// Build validates nothing about the recipe itself (callers run
// recipe.Validate); it resolves the release, bootstraps the image and, only if
// everything succeeded, places the tarball and then the lock. A failure leaves
// no lock, no tarball and no temporary lock, and keeps the work directory.
func (b *Builder) Build(ctx context.Context, r recipe.Recipe, opts Options) (Result, error) {
	goos := opts.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "linux" {
		return Result{}, ErrNotLinux
	}
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	info, err := distro.Lookup(r.Image.Release, r.Image.Arch)
	if err != nil {
		return Result{}, err
	}
	mirror := info.Base
	if opts.Mirror != "" {
		mirror = opts.Mirror
	}
	spec := BootstrapSpec{
		Suite:      info.Suite,
		Sources:    info.Sources(opts.Mirror),
		Include:    MergeInclude(r.Packages.Include),
		Arch:       r.Image.Arch,
		Recommends: true,
		Keyring:    UbuntuKeyring,
	}
	if p, ok := b.Bootstrap.(Preflighter); ok {
		if err := p.Preflight(spec); err != nil {
			return Result{}, err
		}
	}

	root, err := WorkRoot(getenv)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Result{}, fmt.Errorf("creating work root: %w (set XDG_CACHE_HOME to use another location)", err)
	}
	work, err := os.MkdirTemp(root, "build-*")
	if err != nil {
		return Result{}, fmt.Errorf("creating work directory: %w (set XDG_CACHE_HOME to use another location)", err)
	}

	// From here on every failure keeps the work directory: it is the only
	// debugging evidence. Nothing in dist/ or the lock is touched until the
	// bootstrap has fully succeeded.
	res := Result{WorkDir: work}
	lockPath := filepath.Join(opts.Dir, "frostroot.lock")
	tmpLock := lockPath + ".tmp"
	fail := func(err error) (Result, error) {
		os.Remove(tmpLock)
		return res, err
	}

	stage, err := WriteStage(filepath.Join(work, "stage"), r)
	if err != nil {
		return fail(err)
	}
	spec.Hooks = CustomizeHooks(stage)
	spec.TarPath = filepath.Join(work, "image.tar.gz")
	spec.WorkDir = work
	if err := b.Bootstrap.Run(ctx, spec); err != nil {
		return fail(err)
	}

	pkgs, err := readStatus(stage.StatusOut)
	if err != nil {
		return fail(err)
	}
	requested := r.Packages.Include
	if requested == nil {
		requested = []string{}
	}
	lock := recipe.Lockfile{
		Version:          1,
		Distro:           "ubuntu",
		Release:          r.Image.Release,
		Suite:            info.Suite,
		Arch:             r.Image.Arch,
		Mirror:           mirror,
		Sources:          spec.Sources,
		FrostrootVersion: Version,
		Requested:        requested,
		Packages:         pkgs,
	}
	if err := recipe.SaveLock(tmpLock, lock); err != nil {
		return fail(err)
	}

	// mmdebstrap creates its output file before it starts, so existence alone
	// proves nothing.
	if fi, err := os.Stat(spec.TarPath); err != nil || fi.Size() == 0 {
		return fail(fmt.Errorf("bootstrap reported success but left no tarball at %s", spec.TarPath))
	}
	dest := filepath.Join(opts.Dir, export.TarballRelPath(r.Image.Name, r.Image.Release, r.Image.Arch))
	if err := export.Place(spec.TarPath, dest); err != nil {
		return fail(fmt.Errorf("placing tarball: %w", err))
	}
	// The lock goes into place only after the tarball has landed, so a lock
	// never describes an image that does not exist.
	if err := os.Rename(tmpLock, lockPath); err != nil {
		return fail(err)
	}

	res.LockPath = lockPath
	res.TarballPath = dest
	if opts.KeepWork {
		return res, nil
	}
	if err := os.RemoveAll(work); err != nil {
		res.CleanupErr = err
		return res, nil
	}
	res.WorkDir = ""
	return res, nil
}

func readStatus(path string) ([]recipe.LockPackage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("bootstrap did not produce the image's dpkg status: %w", err)
	}
	defer f.Close()
	return ParseDpkgStatus(f)
}
