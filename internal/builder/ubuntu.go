package builder

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"frostroot/internal/distro"
	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// ubuntuBuild builds an Ubuntu image: mmdebstrap installs it with apt from
// the archive, or offline from a flat repository of the vendored files, and
// the lock is read from dpkg's status and apt's marks.
type ubuntuBuild struct {
	bootstrapper Bootstrapper
	imageRecipe  recipe.Recipe
	options      Options
	progress     Progress
	release      distro.Release
	archiveURL   string
	offline      *offlinePlan // nil online
	sourceKeys   map[string]sourceKey
	certificates CertificateSet

	// Set by preflight.
	instant          frozenInstant
	imageSourceLines []string
	bootstrapSpec    BootstrapSpec

	// Set by bootstrap.
	stage             Stage
	installedPackages []recipe.LockPackage
}

// newUbuntuBuild resolves the release and, offline, the lock, and reads the
// keys and certificate authorities the build will trust.
func newUbuntuBuild(bootstrapper Bootstrapper, imageRecipe recipe.Recipe, options Options, progress Progress) (*ubuntuBuild, *offlinePlan, error) {
	release, err := distro.Lookup(imageRecipe.Image.Release, imageRecipe.Image.Arch)
	if err != nil {
		return nil, nil, err
	}
	archiveURL := release.ArchiveURL
	if options.MirrorURL != "" {
		archiveURL = options.MirrorURL
	}
	var offline *offlinePlan
	if options.Offline {
		if options.MirrorURL != "" {
			return nil, nil, errors.New("an offline build takes no mirror: it installs from vendor/debs")
		}
		if offline, err = planOffline(options.RecipeDir, imageRecipe, release); err != nil {
			return nil, nil, err
		}
	}
	// The keys a build will trust are read before anything else is done.
	sourceKeys, err := readSourceKeys(options.RecipeDir, imageRecipe.Sources)
	if err != nil {
		return nil, nil, err
	}
	// So are the certificate authorities, for the same reason.
	certificates, err := ReadCertificates(options.RecipeDir, imageRecipe.CertificatePaths())
	if err != nil {
		return nil, nil, err
	}
	build := &ubuntuBuild{
		bootstrapper: bootstrapper,
		imageRecipe:  imageRecipe,
		options:      options,
		progress:     progress,
		release:      release,
		archiveURL:   archiveURL,
		offline:      offline,
		sourceKeys:   sourceKeys,
		certificates: certificates,
	}
	return build, offline, nil
}

func (u *ubuntuBuild) preflight(workRoot string, instant frozenInstant) error {
	// Apt splits a "deb" line on whitespace and reads options out of
	// brackets, and every build hands it a path under the work root: the
	// signed-by path of an extra source, and the copy:// URL of the
	// vendored pool an offline build installs from. Neither can be quoted,
	// so a work root holding one of these characters is refused before
	// anything is built, whatever the recipe asks for.
	if strings.ContainsAny(workRoot, " \t[]") {
		return fmt.Errorf("%w: %s contains a space or a bracket, which an apt source line cannot carry; set XDG_CACHE_HOME to another directory", ErrBadWorkRoot, workRoot)
	}
	u.instant = instant
	// The lines the image keeps: keys under /etc/apt/keyrings. The lines
	// mmdebstrap gets name the staged keys instead, once the stage exists.
	u.imageSourceLines = SourceLines(u.release, u.options.MirrorURL, u.imageRecipe.Sources, ImageKeyringDir)
	u.bootstrapSpec = BootstrapSpec{
		Suite:             u.release.Suite,
		SourceLines:       u.imageSourceLines,
		Include:           PackagesToInstall(u.imageRecipe),
		Arch:              u.imageRecipe.Image.Arch,
		InstallRecommends: true,
		KeyringPath:       UbuntuArchiveKeyring,
		WorkDir:           workRoot,
		SourceDateEpoch:   instant.epoch,
		Progress:          u.progress,
	}
	if u.offline != nil {
		// Every locked package, so that the outcome does not depend on the
		// priorities in the local index; see the v0.4 spec.
		u.bootstrapSpec.Include = u.offline.packageNames()
		u.bootstrapSpec.Trusted = true
		u.bootstrapSpec.KeyringPath = ""
	}
	if preflighter, ok := u.bootstrapper.(Preflighter); ok {
		if err := preflighter.Preflight(u.bootstrapSpec); err != nil {
			return err
		}
	}
	return nil
}

func (u *ubuntuBuild) bootstrap(ctx context.Context, workDir string) (string, error) {
	offline, options, imageRecipe := u.offline, u.options, u.imageRecipe
	// apt fetches a recipe's extra sources on the build host, so an HTTPS
	// source behind a proxy that inspects TLS needs the authority here too.
	// Offline there is no source to fetch and no network to inspect.
	if offline == nil {
		buildTrustPEM := append(append([]byte(nil), u.certificates.PEM...), options.ExtraTrustPEM...)
		caInfoPath, err := writeAptCaInfo(workDir, buildTrustPEM)
		if err != nil {
			return "", err
		}
		u.bootstrapSpec.CaInfoPath = caInfoPath
		u.bootstrapSpec.Insecure = options.Insecure
	}

	stageOptions := StageOptions{
		RecordForLock: offline == nil,
		SourceLines:   u.imageSourceLines,
		Python:        PythonOptions{Offline: offline != nil, SourceDateEpoch: u.instant.epoch, Insecure: options.Insecure},
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
	if len(u.certificates.Files) > 0 {
		stageOptions.Certificates = u.certificates.Files
		// The recipe's authorities are in the image's own store by the time
		// the Python step runs, so pointing pip at that store is what lets
		// it fetch through the proxy that signs with them.
		stageOptions.Python.TrustImageCertificates = true
	}
	if len(options.ExtraTrustPEM) > 0 {
		stageOptions.ExtraTrust = options.ExtraTrustPEM
		stageOptions.Python.ExtraTrust = true
	}
	if len(u.sourceKeys) > 0 {
		stageOptions.Keys = map[string][]byte{}
		for name, key := range u.sourceKeys {
			stageOptions.Keys[name] = key.binary
		}
	}
	stage, err := WriteStage(filepath.Join(workDir, "stage"), imageRecipe, stageOptions)
	if err != nil {
		return "", err
	}
	switch {
	case offline != nil:
		repositoryDir := filepath.Join(workDir, "pool")
		if err := stageRepository(offline, repositoryDir, u.progress); err != nil {
			return "", err
		}
		if stage.WheelsDir != "" {
			if err := pool.StageFiles(offline.wheelPoolDir, offline.wheelEntries, stage.WheelsDir, nil); err != nil {
				return "", fmt.Errorf("staging the vendored wheels: %w", err)
			}
		}
		u.bootstrapSpec.SourceLines = []string{"deb [trusted=yes] copy://" + repositoryDir + " ./"}
	case len(imageRecipe.Sources) > 0:
		// apt run by mmdebstrap resolves signed-by on the host (see the v0.5
		// spec's spike), so the lines it gets name the staged keys.
		u.bootstrapSpec.SourceLines = SourceLines(u.release, options.MirrorURL, imageRecipe.Sources, stage.KeyringDir)
	}
	u.bootstrapSpec.CustomizeHooks = CustomizeHooks(stage)
	u.bootstrapSpec.TarballPath = filepath.Join(workDir, "image.tar.gz")
	u.bootstrapSpec.WorkDir = workDir
	if err := u.bootstrapper.Run(ctx, u.bootstrapSpec); err != nil {
		return "", err
	}

	installedPackages, err := readDpkgStatus(stage.DpkgStatusPath)
	if err != nil {
		return "", err
	}
	u.stage, u.installedPackages = stage, installedPackages
	return u.bootstrapSpec.TarballPath, nil
}

func (u *ubuntuBuild) checkAgainstLock() (packageCounts, error) {
	if err := compareWithLock(u.offline.lock, u.installedPackages); err != nil {
		return packageCounts{}, err
	}
	counts := packageCounts{installed: len(u.installedPackages)}
	if u.stage.PipListPath != "" {
		installedWheels, err := readPipList(u.stage.PipListPath)
		if err != nil {
			return packageCounts{}, err
		}
		if err := ComparePythonWithLock(u.offline.lock, installedWheels); err != nil {
			return packageCounts{}, err
		}
		counts.python = len(u.offline.lock.PyPI)
	}
	return counts, nil
}

func (u *ubuntuBuild) composeLock() (recipe.Lockfile, packageCounts, error) {
	stage, imageRecipe, installedPackages := u.stage, u.imageRecipe, u.installedPackages
	if err := recordChecksums(stage.AptListsDir, installedPackages, indexOrigins(u.release, u.archiveURL, imageRecipe.Sources)); err != nil {
		return recipe.Lockfile{}, packageCounts{}, err
	}
	autoMarks, err := readExtendedStates(stage.ExtendedStatesPath)
	if err != nil {
		return recipe.Lockfile{}, packageCounts{}, err
	}
	markAutoInstalled(installedPackages, autoMarks, imageRecipe.Image.Arch)
	requestedPackages := imageRecipe.Packages.Include
	if requestedPackages == nil {
		requestedPackages = []string{}
	}
	lock := recipe.Lockfile{
		Version:          1,
		Distro:           string(distro.Ubuntu),
		Release:          imageRecipe.Image.Release,
		Suite:            u.release.Suite,
		Arch:             imageRecipe.Image.Arch,
		Mirror:           u.archiveURL,
		Sources:          u.imageSourceLines,
		FrostrootVersion: Version,
		Requested:        requestedPackages,
		SourceDateEpoch:  u.instant.epoch,
		Repositories:     lockRepositories(u.release, imageRecipe.Sources, u.sourceKeys),
		Certificates:     u.certificates.Locked,
		Packages:         installedPackages,
	}
	counts := packageCounts{installed: len(installedPackages)}
	if stage.PipReportPath != "" {
		pythonResult, err := readPipReport(stage.PipReportPath, imageRecipe.PythonPackages())
		if err != nil {
			return recipe.Lockfile{}, packageCounts{}, err
		}
		lock.Python = &recipe.LockPython{
			Requested:   imageRecipe.PythonPackages(),
			Venv:        PythonVenvPath,
			Interpreter: pythonResult.Interpreter,
			PipVersion:  pythonResult.PipVersion,
			IndexURL:    imageRecipe.PythonIndexURL(),
		}
		// Nothing but TLS stands between a resolve and whoever is on
		// the path, and the hashes below are then what every rebuild
		// verifies against. A lock is fact, and this is one.
		if u.options.Insecure {
			lock.Python.Transport = recipe.TransportUnverified
		}
		// A build resolving from an index named by the recipe installed
		// pip from there too, and only the pinned step's own report says
		// at what URL. Recording the constant's PyPI address instead
		// would send vendor to the host such a network blocks.
		if stage.PipPinReportPath != "" {
			pin, err := readPinReport(stage.PipPinReportPath)
			if err != nil {
				return recipe.Lockfile{}, packageCounts{}, err
			}
			pythonResult.Wheels = withResolvedPin(pythonResult.Wheels, pin)
		}
		lock.PyPI = pythonResult.Wheels
		counts.python = len(pythonResult.Wheels)
	}
	return lock, counts, nil
}
