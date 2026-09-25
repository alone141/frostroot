package builder

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"frostroot/internal/deb"
	"frostroot/internal/distro"
	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// LockFileName is the lock every build writes or, offline, reads, in the
// recipe directory.
const LockFileName = "frostroot.lock"

// Errors of an offline build that the user can fix before any work is done.
// Compare with errors.Is.
var (
	// ErrNoLock means an offline build found no frostroot.lock.
	ErrNoLock = errors.New("no frostroot.lock")
	// ErrLockMismatch means frostroot.toml has changed since frostroot.lock
	// was written, in a way that changes the packages.
	ErrLockMismatch = errors.New("frostroot.lock does not match frostroot.toml")
	// ErrPoolIncomplete means vendor/debs lacks files the lock names, or
	// holds corrupt ones.
	ErrPoolIncomplete = errors.New("vendor/debs is incomplete")
	// ErrImageDiffersFromLock means an offline build produced another set of
	// packages than the lock records. The build fails; nothing is placed.
	ErrImageDiffersFromLock = errors.New("the rebuilt image differs from frostroot.lock")
)

// packageKey identifies one installed package the way the lock does.
type packageKey struct {
	name, version, arch string
}

func keyOf(locked recipe.LockPackage) packageKey {
	return packageKey{name: locked.Name, version: locked.Version, arch: locked.Arch}
}

func (k packageKey) String() string { return k.name + " " + k.version + " " + k.arch }

// offlinePlan is what an offline build knows before any work directory
// exists: the lock it rebuilds and the pool it rebuilds from.
type offlinePlan struct {
	lockPath string
	lock     recipe.Lockfile
	entries  []pool.Entry
	poolDir  string
	// wheelEntries and wheelPoolDir are the Python side, empty for a lock
	// with no Python packages.
	wheelEntries []pool.Entry
	wheelPoolDir string
}

// packageNames returns every locked package name, sorted, for --include.
func (p *offlinePlan) packageNames() []string {
	names := make([]string, 0, len(p.lock.Packages))
	for _, locked := range p.lock.Packages {
		names = append(names, locked.Name)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// planOffline loads the lock in recipeDir and checks that it still describes
// imageRecipe: same release, same architecture, same requested packages.
// The user, sudo, locale, timezone and systemd settings may differ; they are
// provisioned at build time and need no packages.
func planOffline(recipeDir string, imageRecipe recipe.Recipe, release distro.Release) (*offlinePlan, error) {
	lockPath := filepath.Join(recipeDir, LockFileName)
	lock, err := recipe.LoadLock(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w in %s; run frostroot build online first, then frostroot vendor", ErrNoLock, recipeDir)
	}
	if err != nil {
		return nil, err
	}
	entries, err := pool.Manifest(lock)
	if errors.Is(err, pool.ErrNoChecksums) {
		return nil, fmt.Errorf("%w (written by frostroot %s); run frostroot build online once with this version, then frostroot vendor", err, lock.FrostrootVersion)
	}
	if err != nil {
		return nil, err
	}
	// The lock's source lines become the image's sources.list. With none,
	// nothing would be staged in their place, and the line mmdebstrap was
	// given to install from, deb [trusted=yes] copy://<work directory>/pool,
	// would stay in the image: a build host's scratch path, trusted, and
	// different in every rebuild. Every frostroot that wrote a lock wrote the
	// lines, so an empty list is an edited lock.
	if len(lock.Sources) == 0 {
		return nil, fmt.Errorf("%w: it records no sources, so the image's sources.list cannot be written; run frostroot build online, then frostroot vendor", pool.ErrBadLock)
	}
	var differences []string
	if lock.Release != imageRecipe.Image.Release || lock.Suite != release.Suite {
		differences = append(differences, fmt.Sprintf("release %s in the lock, %s in the recipe", lock.Release, imageRecipe.Image.Release))
	}
	if lock.Arch != imageRecipe.Image.Arch {
		differences = append(differences, fmt.Sprintf("arch %s in the lock, %s in the recipe", lock.Arch, imageRecipe.Image.Arch))
	}
	if added, removed := setDifferences(lock.Requested, imageRecipe.Packages.Include); len(added)+len(removed) > 0 {
		if len(added) > 0 {
			differences = append(differences, "packages added to the recipe: "+strings.Join(added, ", "))
		}
		if len(removed) > 0 {
			differences = append(differences, "packages removed from the recipe: "+strings.Join(removed, ", "))
		}
	}
	if added, removed := setDifferences(lockedPythonRequested(lock), normalizedPythonNames(imageRecipe.PythonPackages())); len(added)+len(removed) > 0 {
		if len(added) > 0 {
			differences = append(differences, "python packages added to the recipe: "+strings.Join(added, ", "))
		}
		if len(removed) > 0 {
			differences = append(differences, "python packages removed from the recipe: "+strings.Join(removed, ", "))
		}
	}
	differences = append(differences, repositoryDifferences(lock, imageRecipe.Sources, release)...)
	// The key files are read from beside the recipe and installed as the
	// sources' signed-by keyrings, so an offline rebuild would quietly trust
	// whatever they hold now. An online build has no such check to make: it
	// writes the lock, and a changed key_sha256 is then a line in its diff.
	sourceKeys, err := readSourceKeys(recipeDir, imageRecipe.Sources)
	if err != nil {
		return nil, err
	}
	differences = append(differences, sourceKeyDifferences(lock, sourceKeys)...)
	// So are the certificate files, and for the same reason.
	certificates, err := ReadCertificates(recipeDir, imageRecipe.CertificatePaths())
	if err != nil {
		return nil, err
	}
	differences = append(differences, CheckCertificatesAgainstLock(certificates.Locked, lock.Certificates)...)
	// The same name can be a different project on a different index, so a
	// rebuild that resolved elsewhere is not the image the lock describes.
	if wanted, locked := imageRecipe.PythonIndexURL(), lockedPythonIndexURL(lock); wanted != locked {
		differences = append(differences, fmt.Sprintf("python index_url is %s, the lock resolved from %s", describeIndexURL(wanted), describeIndexURL(locked)))
	}
	if len(differences) > 0 {
		return nil, fmt.Errorf("%w: %s; run frostroot build online, then frostroot vendor", ErrLockMismatch, strings.Join(differences, "; "))
	}
	wheelEntries, err := pool.WheelManifest(lock)
	if err != nil {
		return nil, err
	}
	return &offlinePlan{
		lockPath:     lockPath,
		lock:         lock,
		entries:      entries,
		poolDir:      filepath.Join(recipeDir, filepath.FromSlash(pool.DebsDirName)),
		wheelEntries: wheelEntries,
		wheelPoolDir: filepath.Join(recipeDir, filepath.FromSlash(pool.WheelsDirName)),
	}, nil
}

// lockedPythonIndexURL returns the index the lock resolved from, or "" for
// PyPI, whether or not the lock has a [python] table at all.
func lockedPythonIndexURL(lock recipe.Lockfile) string {
	if lock.Python == nil {
		return ""
	}
	return lock.Python.IndexURL
}

// describeIndexURL names an index for a message, PyPI included.
func describeIndexURL(indexURL string) string {
	if indexURL == "" {
		return "PyPI"
	}
	return indexURL
}

// lockedPythonRequested returns the Python packages the lock's recipe asked
// for, compared the way PyPI compares names.
func lockedPythonRequested(lock recipe.Lockfile) []string {
	if lock.Python == nil {
		return nil
	}
	return normalizedPythonNames(lock.Python.Requested)
}

// normalizedPythonNames returns names as PEP 503 compares them.
func normalizedPythonNames(names []string) []string {
	normalized := make([]string, 0, len(names))
	for _, name := range names {
		normalized = append(normalized, recipe.NormalizePythonName(name))
	}
	return normalized
}

// repositoryDifferences says how the recipe's sources differ from the lock's
// repositories: a source added, removed, or with another URL, suite or
// components changes which packages a build installs. The key file is
// sourceKeyDifferences' to compare: it needs the file read, not the recipe.
func repositoryDifferences(lock recipe.Lockfile, sources []recipe.Source, release distro.Release) []string {
	var differences []string
	inRecipe := map[string]bool{}
	for _, source := range sources {
		inRecipe[source.Name] = true
		locked, found := lock.Repository(source.Name)
		switch {
		case !found:
			differences = append(differences, "source added to the recipe: "+source.Name)
		case locked.URL != strings.TrimRight(source.URL, "/") || locked.Suite != source.SuiteFor(release.Suite) || !slices.Equal(locked.Components, source.ComponentsOrDefault()):
			differences = append(differences, "source changed in the recipe: "+source.Name)
		}
	}
	for _, locked := range lock.Repositories {
		if !inRecipe[locked.Name] {
			differences = append(differences, "source removed from the recipe: "+locked.Name)
		}
	}
	return differences
}

// sourceKeyDifferences says which sources' key files are no longer the bytes
// the lock recorded, one message per source, in the lock's order. A key is
// not only trust: the file is installed into the image as that source's
// signed-by keyring, so an offline rebuild with another key beside the
// recipe would quietly ship an image that trusts someone else — the same
// reason CheckCertificatesAgainstLock exists. A source the lock or the
// recipe lacks is repositoryDifferences' to report and is skipped here, so
// nothing is said twice.
func sourceKeyDifferences(lock recipe.Lockfile, keys map[string]sourceKey) []string {
	var differences []string
	for _, locked := range lock.Repositories {
		key, inRecipe := keys[locked.Name]
		switch {
		case !inRecipe:
		case locked.KeySHA256 == "":
			// Every frostroot that wrote [[repositories]] wrote the digest
			// with it, so a lock without one was edited, and cannot vouch
			// for the key.
			differences = append(differences, fmt.Sprintf("the lock records no key_sha256 for source %s", locked.Name))
		case locked.KeySHA256 != key.sha256:
			differences = append(differences, fmt.Sprintf("the key of source %s changed since the lock was written (lock %s, file %s)", locked.Name, shortDigest(locked.KeySHA256), shortDigest(key.sha256)))
		}
	}
	return differences
}

// setDifferences returns what is only in current and what is only in
// previous, each sorted.
func setDifferences(previous, current []string) (added, removed []string) {
	inPrevious, inCurrent := map[string]bool{}, map[string]bool{}
	for _, name := range previous {
		inPrevious[name] = true
	}
	for _, name := range current {
		inCurrent[name] = true
		if !inPrevious[name] {
			added = append(added, name)
		}
	}
	for _, name := range previous {
		if !inCurrent[name] {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return slices.Compact(added), slices.Compact(removed)
}

// compareWithLock fails when the packages installed in the image are not
// exactly the lock's, naming every difference.
func compareWithLock(lock recipe.Lockfile, installed []recipe.LockPackage) error {
	locked, inImage := map[packageKey]bool{}, map[packageKey]bool{}
	for _, lockedPackage := range lock.Packages {
		locked[keyOf(lockedPackage)] = true
	}
	var onlyInImage, onlyInLock []string
	for _, installedPackage := range installed {
		key := keyOf(installedPackage)
		inImage[key] = true
		if !locked[key] {
			onlyInImage = append(onlyInImage, key.String())
		}
	}
	for key := range locked {
		if !inImage[key] {
			onlyInLock = append(onlyInLock, key.String())
		}
	}
	if len(onlyInImage)+len(onlyInLock) == 0 {
		return nil
	}
	sort.Strings(onlyInImage)
	sort.Strings(onlyInLock)
	var details []string
	if len(onlyInLock) > 0 {
		details = append(details, "in the lock but not in the image: "+strings.Join(onlyInLock, ", "))
	}
	if len(onlyInImage) > 0 {
		details = append(details, "in the image but not in the lock: "+strings.Join(onlyInImage, ", "))
	}
	return fmt.Errorf("%w: %s", ErrImageDiffersFromLock, strings.Join(details, "; "))
}

// packagesIndexPattern matches the file names apt gives Packages indexes in
// /var/lib/apt/lists, uncompressed or gzip-compressed:
// archive.ubuntu.com_ubuntu_dists_noble-updates_main_binary-amd64_Packages.
var packagesIndexPattern = regexp.MustCompile(`_dists_[^_]+_.*_Packages(\.gz)?$`)

// indexedEntry is what an index says about a package, and whose index it
// was.
type indexedEntry struct {
	deb.IndexEntry
	source string
}

// recordChecksums fills SHA256, Size, Filename and Source of every installed
// package from the Packages indexes in listsDir, the copy of the image's
// /var/lib/apt/lists, attributing each index to an origin by its name. Every
// installed package must be listed, every index must belong to a known
// origin, and every index listing a version must agree on its checksum;
// otherwise the build fails, because a lock without a checksum, or with one
// from nowhere, is one vendor cannot act on.
func recordChecksums(listsDir string, installed []recipe.LockPackage, origins []indexOrigin) error {
	indexFiles, err := packagesIndexFiles(listsDir, origins)
	if err != nil {
		return err
	}
	wanted := map[packageKey]int{}
	for index, installedPackage := range installed {
		wanted[keyOf(installedPackage)] = index
	}
	found := map[packageKey]indexedEntry{}
	for _, indexFile := range indexFiles {
		if err := readIndexFile(indexFile.path, func(entry deb.IndexEntry) error {
			key := packageKey{name: entry.Package, version: entry.Version, arch: entry.Architecture}
			if _, isWanted := wanted[key]; !isWanted {
				return nil
			}
			entry.SHA256 = strings.ToLower(entry.SHA256)
			if earlier, seen := found[key]; seen && (earlier.SHA256 != entry.SHA256 || earlier.Size != entry.Size) {
				return fmt.Errorf("the apt indexes disagree about %s: %s (%d bytes) and %s (%d bytes)", key, earlier.SHA256, earlier.Size, entry.SHA256, entry.Size)
			}
			found[key] = indexedEntry{IndexEntry: entry, source: indexFile.origin.source} // later files rank higher
			return nil
		}); err != nil {
			return err
		}
	}
	var unlisted []string
	for index := range installed {
		key := keyOf(installed[index])
		entry, listed := found[key]
		switch {
		case !listed:
			unlisted = append(unlisted, key.String())
		case entry.SHA256 == "" || entry.Size <= 0 || entry.Filename == "":
			unlisted = append(unlisted, key.String()+" (index entry without checksum, size or file name)")
		default:
			installed[index].SHA256 = entry.SHA256
			installed[index].Size = entry.Size
			installed[index].Filename = entry.Filename
			installed[index].Source = entry.source
		}
	}
	if len(unlisted) > 0 {
		sort.Strings(unlisted)
		return fmt.Errorf("the image's apt indexes do not list %d installed package(s), so the lock cannot record their checksums: %s", len(unlisted), strings.Join(unlisted, ", "))
	}
	return nil
}

// indexFile is one Packages index in the lists directory and its origin.
type indexFile struct {
	path   string
	origin indexOrigin
}

// packagesIndexFiles lists the Packages indexes in listsDir, lowest-ranked
// origin first, then by name. An index from no known origin is an error.
func packagesIndexFiles(listsDir string, origins []indexOrigin) ([]indexFile, error) {
	directoryEntries, err := os.ReadDir(listsDir)
	if err != nil {
		return nil, fmt.Errorf("the bootstrap did not copy out the image's apt indexes: %w", err)
	}
	var indexFiles []indexFile
	for _, directoryEntry := range directoryEntries {
		if directoryEntry.IsDir() || !packagesIndexPattern.MatchString(directoryEntry.Name()) {
			continue
		}
		origin, known := originOf(directoryEntry.Name(), origins)
		if !known {
			return nil, fmt.Errorf("the apt index %s belongs to no source of this build", directoryEntry.Name())
		}
		indexFiles = append(indexFiles, indexFile{path: filepath.Join(listsDir, directoryEntry.Name()), origin: origin})
	}
	if len(indexFiles) == 0 {
		return nil, fmt.Errorf("no Packages index in %s", listsDir)
	}
	slices.SortStableFunc(indexFiles, func(left, right indexFile) int {
		if left.origin.rank != right.origin.rank {
			return left.origin.rank - right.origin.rank
		}
		return strings.Compare(left.path, right.path)
	})
	return indexFiles, nil
}

// readIndexFile streams the entries of one index, gunzipping if its name
// says so.
func readIndexFile(path string, visit func(deb.IndexEntry) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }() // read-only: closing cannot lose data
	var reader io.Reader = file
	if strings.HasSuffix(path, ".gz") {
		gzipReader, err := gzip.NewReader(file)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		reader = gzipReader
	}
	if err := deb.ReadIndexEntries(reader, visit); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}
