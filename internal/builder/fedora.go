package builder

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
)

// FedoraBootstrapper builds Fedora images. Mkosi is the real one; tests use
// fakes that write the files it would.
type FedoraBootstrapper interface {
	Preflight(ctx context.Context, spec FedoraSpec) error
	Run(ctx context.Context, spec FedoraSpec) error
}

// fedoraPackageCacheName is the directory under the work root that keeps
// mkosi's downloads between builds, so that only the first build of a
// release fetches every package.
const fedoraPackageCacheName = "fedora-packages"

// fedoraBuild builds a Fedora image: mkosi makes the tools tree with the
// host's dnf, then the image with the tools tree's dnf5, and the lock is read
// from what dnf5 says it installed.
type fedoraBuild struct {
	bootstrapper FedoraBootstrapper
	imageRecipe  recipe.Recipe
	options      Options
	progress     Progress
	release      distro.FedoraRelease
	offline      *offlinePlan // nil online

	// Set by preflight.
	instant  frozenInstant
	cacheDir string

	// Set by bootstrap.
	spec      FedoraSpec
	installed []fedoraInstalled
}

// newFedoraBuild resolves the release and, for an offline build, the plan
// it rebuilds from.
func newFedoraBuild(bootstrapper FedoraBootstrapper, imageRecipe recipe.Recipe, options Options, progress Progress) (*fedoraBuild, *offlinePlan, error) {
	release, err := distro.LookupFedora(imageRecipe.Image.Release, imageRecipe.Image.Arch)
	if err != nil {
		return nil, nil, err
	}
	if options.MirrorURL != "" {
		return nil, nil, fmt.Errorf("%w: --mirror replaces Ubuntu's archive; a Fedora build finds Fedora's mirrors through its metalinks", ErrFedoraOption)
	}
	if bootstrapper == nil {
		return nil, nil, errors.New("this frostroot was built without a Fedora bootstrapper")
	}
	build := &fedoraBuild{
		bootstrapper: bootstrapper,
		imageRecipe:  imageRecipe,
		options:      options,
		progress:     progress,
		release:      release,
	}
	if options.Offline {
		if build.offline, err = planFedoraOffline(options.RecipeDir, imageRecipe, release); err != nil {
			return nil, nil, err
		}
	}
	return build, build.offline, nil
}

func (f *fedoraBuild) preflight(ctx context.Context, workRoot string, instant frozenInstant) error {
	f.instant = instant
	f.cacheDir = filepath.Join(workRoot, fedoraPackageCacheName)
	return f.bootstrapper.Preflight(ctx, FedoraSpec{WorkDir: workRoot})
}

func (f *fedoraBuild) bootstrap(ctx context.Context, workDir string) (string, error) {
	spec := FedoraSpec{
		WorkDir:         workDir,
		ToolsConfigDir:  filepath.Join(workDir, "mkosi-tools"),
		ImageConfigDir:  filepath.Join(workDir, "mkosi-image"),
		CacheDir:        f.cacheDir,
		ToolsOutputDir:  filepath.Join(workDir, "tools"),
		ToolsRecordPath: filepath.Join(workDir, fedoraToolsInstalledFile),
		ImageOutputDir:  filepath.Join(workDir, "image"),
		SourceDateEpoch: f.instant.epoch,
		TarballPath:     filepath.Join(workDir, "image.tar.gz"),
		Progress:        f.progress,
	}
	tools := fedoraContent{
		repositories: fedoraRepositories{release: f.release, insecure: f.options.Insecure},
		packages:     fedoraToolsPackages,
	}
	image := fedoraContent{
		repositories: fedoraRepositories{release: f.release, insecure: f.options.Insecure},
		packages:     FedoraPackagesToInstall(f.imageRecipe),
	}
	if len(f.options.ExtraTrustPEM) > 0 {
		tools.repositories.caBundle = "/" + fedoraTrustDir + "/ca-bundle.pem"
		image.repositories.caBundle = tools.repositories.caBundle
	}
	if f.offline != nil {
		// Every locked package, by name and version, from local
		// repositories that hold exactly the lock's files, into a cache of
		// the build's own; nothing is fetched, so nothing is trusted but
		// the key.
		spec.Offline = true
		spec.CacheDir = filepath.Join(workDir, fedoraOfflineCacheName)
		spec.LocalMirror = filepath.Join(workDir, fedoraLocalMirrorName)
		dirs, toolsLocal, imageLocal, err := stageFedoraRepositories(f.offline, f.release, spec.LocalMirror, f.progress)
		if err != nil {
			return "", err
		}
		spec.LocalRepositories = dirs
		tools = fedoraContent{repositories: fedoraRepositories{release: f.release, local: toolsLocal}, packages: fedoraPackageSpecs(f.offline.lock.Tools)}
		image = fedoraContent{
			repositories: fedoraRepositories{release: f.release, local: imageLocal},
			packages:     fedoraPackageSpecs(f.offline.lock.Packages),
			reasons:      renderFedoraReasons(f.offline.lock.Packages),
		}
	}
	if err := f.writeTools(spec, tools); err != nil {
		return "", err
	}
	if err := f.writeImage(spec, image); err != nil {
		return "", err
	}
	if err := f.bootstrapper.Run(ctx, spec); err != nil {
		return "", err
	}
	installed, err := readFedoraInstalled(filepath.Join(spec.ImageOutputDir, fedoraInstalledFile))
	if err != nil {
		return "", err
	}
	f.spec, f.installed = spec, installed
	return spec.TarballPath, nil
}

// fedoraContent is what one of mkosi's two builds installs, and from where.
type fedoraContent struct {
	repositories fedoraRepositories
	packages     []string
	// reasons is dnf5's packages.toml as the lock records it, which an
	// offline image build puts back; "" online.
	reasons string
}

// writeTools writes mkosi's configuration for the tools tree.
func (f *fedoraBuild) writeTools(spec FedoraSpec, tools fedoraContent) error {
	if err := f.writePackageManagerTree(filepath.Join(spec.ToolsConfigDir, fedoraPackageManagerTree), tools.repositories, false); err != nil {
		return err
	}
	conf := renderFedoraMkosiConf(fedoraMkosiConf{release: f.release, packages: tools.packages})
	if err := writeFedoraFile(filepath.Join(spec.ToolsConfigDir, "mkosi.conf"), []byte(conf), 0o644); err != nil {
		return err
	}
	return writeFedoraFile(filepath.Join(spec.ToolsConfigDir, fedoraFinalizeName), []byte(fedoraToolsFinalize), 0o755)
}

// writeImage writes mkosi's configuration for the image, the files its
// post-installation script installs and, offline, the reasons its finalize
// script puts back.
func (f *fedoraBuild) writeImage(spec FedoraSpec, image fedoraContent) error {
	if err := f.writePackageManagerTree(filepath.Join(spec.ImageConfigDir, fedoraPackageManagerTree), image.repositories, true); err != nil {
		return err
	}
	conf := renderFedoraMkosiConf(fedoraMkosiConf{release: f.release, image: true, recommend: true, packages: image.packages})
	provision, err := RenderFedoraProvisionScript(f.imageRecipe)
	if err != nil {
		return err
	}
	finalize, err := renderFedoraImageFinalize(f.release, image.reasons != "")
	if err != nil {
		return err
	}
	files := map[string]string{
		"mkosi.conf":        conf,
		fedoraProvisionName: provision,
		fedoraFinalizeName:  finalize,
		"wsl.conf":          RenderWSLConf(f.imageRecipe),
	}
	if sudoers := RenderSudoers(f.imageRecipe); sudoers != "" {
		files["sudoers"] = sudoers
	}
	if image.reasons != "" {
		files[fedoraReasonsFile] = image.reasons
	}
	for name, content := range files {
		mode := os.FileMode(0o644)
		if name == fedoraProvisionName || name == fedoraFinalizeName {
			mode = 0o755 // mkosi runs its scripts directly
		}
		if err := writeFedoraFile(filepath.Join(spec.ImageConfigDir, name), []byte(content), mode); err != nil {
			return err
		}
	}
	return nil
}

// writePackageManagerTree writes the tree mkosi configures dnf from: the
// repositories, the release's key beside them, and the build's own trust.
// The repository file must be called mkosi.repo, or mkosi writes one of its
// own beside it, with Fedora's key fetched from the network.
func (f *fedoraBuild) writePackageManagerTree(tree string, repositories fedoraRepositories, imageBuild bool) error {
	repoFile, err := renderFedoraRepos(repositories)
	if err != nil {
		return err
	}
	dnfConf := "[main]\n"
	if imageBuild {
		// dnf5 loads no file lists unless asked, and some dependencies
		// are on paths; mkosi asks the same when it writes its own.
		dnfConf += "optional_metadata_types=filelists\n"
	}
	files := map[string][]byte{
		"etc/yum.repos.d/mkosi.repo":                           []byte(repoFile),
		"etc/dnf/dnf.conf":                                     []byte(dnfConf),
		path.Join(fedoraKeysDir, fedoraKeyFileName(f.release)): f.release.Key,
	}
	if repositories.caBundle != "" {
		hostPEM, err := os.ReadFile(HostTrustPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("reading %s: %w", HostTrustPath, err)
		}
		// The host's store first: sslcacert replaces dnf's store, and a
		// network that inspects some hosts must still verify the rest.
		files[path.Join(fedoraTrustDir, "ca-bundle.pem")] = slices.Concat(hostPEM, f.options.ExtraTrustPEM)
	}
	for name, content := range files {
		if err := writeFedoraFile(filepath.Join(tree, filepath.FromSlash(name)), content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeFedoraFile writes a file mkosi reads, creating the directories it is
// in. Files and directories alike are readable by anyone, whatever the
// umask, because an unprivileged mkosi reads them as a subordinate uid.
func writeFedoraFile(filePath string, content []byte, mode os.FileMode) error {
	if err := mkdirAllReadable(filepath.Dir(filePath)); err != nil {
		return err
	}
	if err := os.WriteFile(filePath, content, mode); err != nil {
		return err
	}
	return os.Chmod(filePath, mode)
}

// mkdirAllReadable creates dir and its missing parents with mode 0755,
// whatever the umask.
func mkdirAllReadable(dir string) error {
	var missing []string
	for current := filepath.Clean(dir); ; current = filepath.Dir(current) {
		if _, err := os.Stat(current); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
		if filepath.Dir(current) == current {
			break
		}
	}
	for index := len(missing) - 1; index >= 0; index-- {
		if err := os.Mkdir(missing[index], 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := os.Chmod(missing[index], 0o755); err != nil {
			return err
		}
	}
	return nil
}

// checkAgainstLock fails when the rebuilt image, or the tools tree that made
// it, holds other packages than the lock, or took one from another
// repository.
func (f *fedoraBuild) checkAgainstLock() (packageCounts, error) {
	lock := f.offline.lock
	installed := make([]recipe.LockPackage, 0, len(f.installed))
	for _, listed := range f.installed {
		installed = append(installed, listed.lockPackage())
	}
	if err := compareWithLock(lock, installed); err != nil {
		return packageCounts{}, err
	}
	repositoryOf := map[packageKey]string{}
	for _, locked := range lock.Packages {
		repositoryOf[keyOf(locked)] = locked.Source
	}
	var elsewhere []string
	for _, rebuilt := range installed {
		if locked := repositoryOf[keyOf(rebuilt)]; rebuilt.Source != locked {
			elsewhere = append(elsewhere, fmt.Sprintf("%s from %s, locked from %s", keyOf(rebuilt), rebuilt.Source, locked))
		}
	}
	if len(elsewhere) > 0 {
		return packageCounts{}, fmt.Errorf("%w: installed from another repository: %s", ErrImageDiffersFromLock, strings.Join(elsewhere, ", "))
	}
	toolsListed, err := readFedoraToolsInstalled(f.spec.ToolsRecordPath)
	if err != nil {
		return packageCounts{}, err
	}
	tools := make([]recipe.LockPackage, 0, len(toolsListed))
	for _, listed := range toolsListed {
		tools = append(tools, listed.lockPackage())
	}
	if details := packageSetDifferences(lock.Tools, tools, "the tools tree"); len(details) > 0 {
		return packageCounts{}, fmt.Errorf("%w: %s", ErrImageDiffersFromLock, strings.Join(details, "; "))
	}
	return packageCounts{installed: len(installed)}, nil
}

func (f *fedoraBuild) composeLock() (recipe.Lockfile, packageCounts, error) {
	locations, err := readFedoraLocations(filepath.Join(f.spec.ImageOutputDir, fedoraLocationsFile))
	if err != nil {
		return recipe.Lockfile{}, packageCounts{}, err
	}
	var packages []recipe.LockPackage
	for _, installed := range f.installed {
		found, ok := locations[installed.nevra]
		if !ok {
			return recipe.Lockfile{}, packageCounts{}, fmt.Errorf("dnf5 installed %s and does not say where it found it", installed.nevra)
		}
		if found.repository != installed.fromRepo {
			return recipe.Lockfile{}, packageCounts{}, fmt.Errorf("dnf5 installed %s from %s and finds it in %s", installed.nevra, installed.fromRepo, found.repository)
		}
		filename, err := fedoraFilename(found.location)
		if err != nil {
			return recipe.Lockfile{}, packageCounts{}, fmt.Errorf("%s: %w", installed.nevra, err)
		}
		locked := installed.lockPackage()
		locked.Filename = filename
		locked.SHA256, locked.Size, err = f.cachedPackage("libdnf5", installed.fromRepo, path.Base(filename))
		if err != nil {
			return recipe.Lockfile{}, packageCounts{}, err
		}
		packages = append(packages, locked)
	}
	tools, err := f.lockTools()
	if err != nil {
		return recipe.Lockfile{}, packageCounts{}, err
	}
	keyDigest := sha256.Sum256(f.release.Key)
	var repositories []recipe.LockRepository
	for _, repository := range f.release.Repositories {
		repositories = append(repositories, recipe.LockRepository{Name: repository.ID, URL: repository.BaseURL, KeySHA256: hex.EncodeToString(keyDigest[:])})
	}
	requested := f.imageRecipe.Packages.Include
	if requested == nil {
		requested = []string{}
	}
	sortLockPackages(packages)
	sortLockPackages(tools)
	lock := recipe.Lockfile{
		Version:          1,
		Distro:           string(distro.Fedora),
		Release:          f.release.Version,
		Arch:             f.imageRecipe.Image.Arch,
		FrostrootVersion: Version,
		Requested:        requested,
		SourceDateEpoch:  f.instant.epoch,
		Repositories:     repositories,
		Packages:         packages,
		Tools:            tools,
	}
	return lock, packageCounts{installed: len(packages)}, nil
}

// lockTools returns the tools tree's packages, each with the file the host's
// dnf downloaded it as. Its repository is where it was downloaded from.
func (f *fedoraBuild) lockTools() ([]recipe.LockPackage, error) {
	listed, err := readFedoraToolsInstalled(f.spec.ToolsRecordPath)
	if err != nil {
		return nil, err
	}
	var tools []recipe.LockPackage
	for _, installed := range listed {
		fileName := installed.fileName()
		repository, err := f.cachedRepository("dnf", fileName)
		if err != nil {
			return nil, err
		}
		locked := installed.lockPackage()
		locked.Source = repository
		locked.Filename = fedoraLayoutPath(fileName)
		locked.SHA256, locked.Size, err = f.cachedPackage("dnf", repository, fileName)
		if err != nil {
			return nil, err
		}
		tools = append(tools, locked)
	}
	return tools, nil
}

// cachedRepository returns the repository whose download of fileName the
// package manager's cache holds.
func (f *fedoraBuild) cachedRepository(packageManager, fileName string) (string, error) {
	for _, repository := range f.release.Repositories {
		if _, _, err := f.cachedPackage(packageManager, repository.ID, fileName); err == nil {
			return repository.ID, nil
		}
	}
	return "", fmt.Errorf("the tools tree holds %s, which no repository's download in %s is", fileName, f.cacheDir)
}

// cachedPackage returns the digest and size of fileName as the package
// manager downloaded it from repository into mkosi's cache: the very file
// that was installed.
func (f *fedoraBuild) cachedPackage(packageManager, repository, fileName string) (string, int64, error) {
	// The cache names each repository's directory by a hash of its
	// configuration, which frostroot does not reproduce.
	matches, err := filepath.Glob(filepath.Join(f.cacheDir, "cache", packageManager, repository+"-*", "packages", fileName))
	if err != nil || len(matches) == 0 {
		return "", 0, fmt.Errorf("%s from %s is not in %s", fileName, repository, f.cacheDir)
	}
	file, err := os.Open(matches[0])
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }() // read-only: closing cannot lose data
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	if err != nil {
		return "", 0, fmt.Errorf("reading %s: %w", matches[0], err)
	}
	return hex.EncodeToString(digest.Sum(nil)), size, nil
}

// fedoraInstalled is one package dnf5 says the image holds.
type fedoraInstalled struct {
	nevra     string // as dnf5 prints it: name-epoch:version-release.arch
	name      string
	epoch     string
	version   string // version-release, without the epoch
	arch      string
	reason    string // dnf5's: User, Dependency or Weak Dependency
	fromRepo  string
	sourceRPM string
}

// fileName returns the package's file name, which carries no epoch.
func (p fedoraInstalled) fileName() string {
	return p.name + "-" + p.version + "." + p.arch + ".rpm"
}

// lockPackage returns the package as the lock records it: its version with
// the epoch in front when there is one, as dnf prints it.
func (p fedoraInstalled) lockPackage() recipe.LockPackage {
	version := p.version
	if p.epoch != "" && p.epoch != "0" {
		version = p.epoch + ":" + version
	}
	return recipe.LockPackage{Name: p.name, Version: version, Arch: p.arch, Reason: p.reason, Source: p.fromRepo, SourceRPM: p.sourceRPM}
}

// dnf5's reasons, the only ones a lock may carry.
var fedoraReasons = []string{"User", "Dependency", "Weak Dependency"}

// readFedoraInstalled reads what the finalize script recorded: one
// full_nevra|reason|from_repo|sourcerpm line per package. The key rpm
// imported is not a package and is left out.
func readFedoraInstalled(recordPath string) ([]fedoraInstalled, error) {
	lines, err := readRecordLines(recordPath, 4)
	if err != nil {
		return nil, err
	}
	var packages []fedoraInstalled
	for _, fields := range lines {
		installed, err := parseFullNEVRA(fields[0])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", recordPath, err)
		}
		if installed.name == "gpg-pubkey" {
			continue
		}
		if !slices.Contains(fedoraReasons, fields[1]) {
			return nil, fmt.Errorf("%s: %s has reason %q, which is not one of dnf5's", recordPath, fields[0], fields[1])
		}
		installed.reason, installed.fromRepo, installed.sourceRPM = fields[1], fields[2], fields[3]
		packages = append(packages, installed)
	}
	if len(packages) == 0 {
		return nil, fmt.Errorf("%s lists no packages", recordPath)
	}
	return packages, nil
}

// readFedoraToolsInstalled reads the tools tree's record: one
// name|epoch|version|release|arch|sourcerpm line per package, as rpm lists
// them.
func readFedoraToolsInstalled(recordPath string) ([]fedoraInstalled, error) {
	lines, err := readRecordLines(recordPath, 6)
	if err != nil {
		return nil, err
	}
	var packages []fedoraInstalled
	for _, fields := range lines {
		if fields[0] == "gpg-pubkey" {
			continue
		}
		packages = append(packages, fedoraInstalled{
			name: fields[0], epoch: fields[1], version: fields[2] + "-" + fields[3], arch: fields[4], sourceRPM: fields[5],
		})
	}
	if len(packages) == 0 {
		return nil, fmt.Errorf("%s lists no packages", recordPath)
	}
	return packages, nil
}

// fedoraLocation is where dnf5 found one package.
type fedoraLocation struct {
	repository string
	location   string // a URL on the mirror or local repository it used
}

// readFedoraLocations reads the finalize script's full_nevra|repoid|location
// lines.
func readFedoraLocations(recordPath string) (map[string]fedoraLocation, error) {
	lines, err := readRecordLines(recordPath, 3)
	if err != nil {
		return nil, err
	}
	locations := map[string]fedoraLocation{}
	for _, fields := range lines {
		locations[fields[0]] = fedoraLocation{repository: fields[1], location: fields[2]}
	}
	return locations, nil
}

// readRecordLines reads a record a script wrote: lines of fields separated
// by |, each line with exactly fields of them.
func readRecordLines(recordPath string, fields int) ([][]string, error) {
	file, err := os.Open(recordPath)
	if err != nil {
		return nil, fmt.Errorf("the build did not record what it installed: %w", err)
	}
	defer func() { _ = file.Close() }() // read-only: closing cannot lose data
	var lines [][]string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		split := strings.Split(line, "|")
		if len(split) != fields {
			return nil, fmt.Errorf("%s: %q is not %d fields", recordPath, line, fields)
		}
		lines = append(lines, split)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", recordPath, err)
	}
	return lines, nil
}

// parseFullNEVRA splits dnf5's name-epoch:version-release.arch.
func parseFullNEVRA(nevra string) (fedoraInstalled, error) {
	rest, arch, ok := cutLast(nevra, ".")
	if !ok {
		return fedoraInstalled{}, fmt.Errorf("%q is not name-epoch:version-release.arch", nevra)
	}
	rest, release, ok := cutLast(rest, "-")
	if !ok {
		return fedoraInstalled{}, fmt.Errorf("%q is not name-epoch:version-release.arch", nevra)
	}
	name, epochVersion, ok := cutLast(rest, "-")
	if !ok {
		return fedoraInstalled{}, fmt.Errorf("%q is not name-epoch:version-release.arch", nevra)
	}
	epoch, version, ok := strings.Cut(epochVersion, ":")
	if !ok {
		epoch, version = "0", epochVersion
	}
	if name == "" || version == "" || release == "" || arch == "" {
		return fedoraInstalled{}, fmt.Errorf("%q is not name-epoch:version-release.arch", nevra)
	}
	return fedoraInstalled{nevra: nevra, name: name, epoch: epoch, version: version + "-" + release, arch: arch}, nil
}

// cutLast is strings.Cut around the last separator.
func cutLast(text, separator string) (before, after string, found bool) {
	index := strings.LastIndex(text, separator)
	if index < 0 {
		return text, "", false
	}
	return text[:index], text[index+len(separator):], true
}

// fedoraLayoutPath is where a Fedora repository keeps a package file:
// Packages/, then the name's first letter, lowercased.
func fedoraLayoutPath(fileName string) string {
	return "Packages/" + strings.ToLower(fileName[:1]) + "/" + fileName
}

// fedoraFilename returns the path of a package below its repository's base
// URL, from the URL dnf5 found it at, which the lock records so that vendor
// can fetch it from Fedora's own server. dnf5 names a mirror's URL, not the
// base URL, so the path is Fedora's layout, checked against it.
func fedoraFilename(location string) (string, error) {
	parsed, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("dnf5 found it at %q, which is not a URL", location)
	}
	fileName := path.Base(parsed.Path)
	if !strings.HasSuffix(fileName, ".rpm") || fileName == ".rpm" {
		return "", fmt.Errorf("dnf5 found it at %q, which is not an rpm file", location)
	}
	filename := fedoraLayoutPath(fileName)
	if !strings.HasSuffix(parsed.Path, "/"+filename) {
		return "", fmt.Errorf("dnf5 found it at %q, not below Packages/ where Fedora keeps it", location)
	}
	return filename, nil
}

// sortLockPackages sorts by name, then architecture, then version.
func sortLockPackages(packages []recipe.LockPackage) {
	slices.SortFunc(packages, func(a, b recipe.LockPackage) int {
		return strings.Compare(a.Name+"\x00"+a.Arch+"\x00"+a.Version, b.Name+"\x00"+b.Arch+"\x00"+b.Version)
	})
}
