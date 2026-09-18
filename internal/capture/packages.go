package capture

import (
	"crypto/md5" // dpkg records conffile checksums as MD5; this only compares against them
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"frostroot/internal/builder"
	"frostroot/internal/recipe"
)

// Package bookkeeping files, relative to the root.
const (
	dpkgStatusPath     = "var/lib/dpkg/status"
	dpkgInfoDir        = "var/lib/dpkg/info"
	extendedStatesPath = "var/lib/apt/extended_states"
	aptListsDir        = "var/lib/apt/lists"
	aptSourcesPath     = "etc/apt/sources.list"
	aptSourcesDir      = "etc/apt/sources.list.d"
)

// Priorities that --variant=important installs without being asked.
var basePriorities = []string{"required", "important"}

// metapackages pull in sets frostroot either provides or reports on its
// own; listing them in a recipe would hide what they contain.
var metapackages = []string{"ubuntu-minimal", "ubuntu-standard", "ubuntu-wsl", "ubuntu-server", "ubuntu-desktop", "ubuntu-desktop-minimal"}

// conffile is one configuration file a package owns, with the checksum of
// the version the package shipped.
type conffile struct {
	path string
	md5  string
}

// installedPackage is what capture needs from a dpkg status stanza.
type installedPackage struct {
	name      string
	priority  string
	arch      string
	conffiles []conffile
}

// installedPackages reads the dpkg status and returns the packages whose
// status word is "installed", sorted by name.
func (root systemRoot) installedPackages() ([]installedPackage, bool) {
	file, err := os.Open(root.path(dpkgStatusPath))
	if err != nil {
		return nil, false
	}
	defer func() { _ = file.Close() }() // read-only
	var packages []installedPackage
	for _, entry := range readStanzas(file) {
		statusWords := strings.Fields(entry["Status"])
		if len(statusWords) != 3 || statusWords[2] != "installed" {
			continue
		}
		installed := installedPackage{name: entry["Package"], priority: entry["Priority"], arch: entry["Architecture"]}
		for _, line := range continuationLines(entry["Conffiles"]) {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] != "obsolete" {
				installed.conffiles = append(installed.conffiles, conffile{path: fields[0], md5: fields[1]})
			}
		}
		packages = append(packages, installed)
	}
	slices.SortFunc(packages, func(a, b installedPackage) int { return strings.Compare(a.name, b.name) })
	return packages, true
}

// autoInstalled returns the packages apt marked as installed automatically,
// to satisfy a dependency. found is false when apt kept no record.
func (root systemRoot) autoInstalled() (auto map[string]bool, found bool) {
	file, err := os.Open(root.path(extendedStatesPath))
	if err != nil {
		return nil, false
	}
	defer func() { _ = file.Close() }() // read-only
	auto = map[string]bool{}
	for _, entry := range readStanzas(file) {
		if entry["Auto-Installed"] == "1" {
			auto[entry["Package"]] = true
		}
	}
	return auto, len(auto) > 0
}

// Packages that belong to the machine rather than to the image: a kernel, a
// bootloader, firmware, drivers and the tools that assemble a disk. An
// installer marks them manual, so they read as packages someone asked for,
// but WSL brings its own kernel and has no bootloader and no disks to
// assemble. In an image they are dead weight, and their maintainer scripts
// (update-initramfs, grub hooks) make every build slower.
//
// Matched by whole name, by prefix and by suffix rather than by a bare
// "linux-" prefix, because linux-tools-* is perf and friends, which are
// useful inside WSL and must not be swept up with the rest.
var (
	machinePackageNames = []string{
		"linux-firmware", "shim-signed", "ubuntu-drivers-common", "efibootmgr",
		"os-prober", "initramfs-tools", "cryptsetup-initramfs", "mdadm", "lvm2",
	}
	machinePackagePrefixes = []string{
		"linux-image-", "linux-headers-", "linux-modules-", "linux-generic",
		"grub-", "nvidia-driver-", "nvidia-dkms-",
	}
	machinePackageSuffixes = []string{"-microcode"}
)

// belongsToTheMachine reports whether a package describes the hardware the
// system runs on rather than the software an image should hold.
func belongsToTheMachine(name string) bool {
	if slices.Contains(machinePackageNames, name) {
		return true
	}
	for _, prefix := range machinePackagePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	for _, suffix := range machinePackageSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// requestedPackages returns the packages someone asked for, in name order:
// installed, not automatic, not part of the base a build provides anyway.
// dropped lists names that fail the recipe's package rule, and machine lists
// the ones that belong to the hardware; both are reported rather than
// written, since the form's free-text field has no "present but unselected".
func requestedPackages(installed []installedPackage, auto map[string]bool) (requested, dropped, machine []string) {
	for _, pkg := range installed {
		switch {
		case auto[pkg.name],
			slices.Contains(basePriorities, pkg.priority),
			slices.Contains(builder.EssentialPackages, pkg.name),
			slices.Contains(metapackages, pkg.name):
			continue
		}
		if recipe.CheckPackageName(pkg.name) != nil {
			dropped = append(dropped, pkg.name)
			continue
		}
		if belongsToTheMachine(pkg.name) {
			machine = append(machine, pkg.name)
			continue
		}
		requested = append(requested, pkg.name)
	}
	return requested, dropped, machine
}

// dpkgArchitecture returns the architecture of the dpkg package itself,
// which is the system's.
func dpkgArchitecture(installed []installedPackage) string {
	for _, pkg := range installed {
		if pkg.name == "dpkg" {
			return pkg.arch
		}
	}
	return ""
}

// ownedPaths returns every path some installed package owns, from the
// .list files in the dpkg info directory.
func (root systemRoot) ownedPaths() (map[string]bool, bool) {
	entries, err := os.ReadDir(root.path(dpkgInfoDir))
	if err != nil {
		return nil, false
	}
	owned := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".list") {
			continue
		}
		content, ok := root.readText(dpkgInfoDir + "/" + entry.Name())
		if !ok {
			continue
		}
		for _, line := range strings.Split(content, "\n") {
			if line != "" {
				owned[line] = true
			}
		}
	}
	return owned, true
}

// modifiedConffiles returns the configuration files whose content no longer
// matches the checksum dpkg recorded: the ones somebody edited.
func (root systemRoot) modifiedConffiles(installed []installedPackage) []string {
	var modified []string
	for _, pkg := range installed {
		for _, file := range pkg.conffiles {
			content, err := os.ReadFile(root.path(file.path))
			if err != nil {
				continue // deleted or unreadable: nothing to compare
			}
			sum := md5.Sum(content)
			if hex.EncodeToString(sum[:]) != file.md5 {
				modified = append(modified, file.path)
			}
		}
	}
	slices.Sort(modified)
	return modified
}

// ubuntuArchiveHosts are the hosts Ubuntu's own archive lives on, as they
// appear at the start of apt's index file names.
var ubuntuArchiveHosts = []string{"archive.ubuntu.com", "security.ubuntu.com", "ports.ubuntu.com", "esm.ubuntu.com"}

// isUbuntuArchive reports whether an apt index file name comes from Ubuntu's
// archive rather than a third-party source. Country mirrors are
// "<cc>.archive.ubuntu.com".
func isUbuntuArchive(indexFileName string) bool {
	host, _, _ := strings.Cut(indexFileName, "_")
	for _, archiveHost := range ubuntuArchiveHosts {
		if host == archiveHost || strings.HasSuffix(host, "."+archiveHost) {
			return true
		}
	}
	return false
}

// aptIndex identifies the repository one apt index file came from: the base
// URL as apt renders it in a file name, and the suite. The host alone will
// not do, because every Launchpad PPA shares ppa.launchpadcontent.net, and
// packages.microsoft.com and deb.nodesource.com serve several repositories
// each; keying on the host lets one carried source vouch for all of them.
type aptIndex struct {
	prefix string
	suite  string
}

// Describe renders an index the way the report should name it: the URL apt
// built the file name from, and the suite.
func (index aptIndex) Describe() string {
	path := strings.ReplaceAll(index.prefix, "_", "/")
	if decoded, err := url.PathUnescape(path); err == nil {
		path = decoded
	}
	return path + " " + index.suite
}

// aptIndexOf reads an apt list file name back into the repository it came
// from. Apt writes "<prefix>_dists_<suite>_<component>_binary-<arch>_Packages"
// and percent-encodes any underscore of its own, so every underscore here
// separates two segments and the split is exact.
func aptIndexOf(indexFileName string) (aptIndex, bool) {
	fields := strings.Split(indexFileName, "_")
	for position, field := range fields {
		// The first "dists" wins: a repository whose own path ends in
		// "dists" would otherwise take the suite from the wrong place.
		if field == "dists" && position > 0 && position+1 < len(fields) {
			return aptIndex{prefix: strings.Join(fields[:position], "_"), suite: fields[position+1]}, true
		}
	}
	return aptIndex{}, false
}

// packageOrigins says, for every package listed in some apt index, whether
// Ubuntu's archive offers it and which third-party repositories do. The map
// is nil when there are no indexes at all (apt update never ran).
type packageOrigins struct {
	inUbuntu   map[string]bool
	thirdParty map[string][]aptIndex // package name to the indexes offering it
	hasIndexes bool
}

func (root systemRoot) packageOrigins() packageOrigins {
	origins := packageOrigins{inUbuntu: map[string]bool{}, thirdParty: map[string][]aptIndex{}}
	entries, err := os.ReadDir(root.path(aptListsDir))
	if err != nil {
		return origins
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_Packages") {
			continue
		}
		index, named := aptIndexOf(entry.Name())
		if !named {
			continue // not a dists index: nothing to attribute it to
		}
		file, err := os.Open(root.path(aptListsDir + "/" + entry.Name()))
		if err != nil {
			continue
		}
		origins.hasIndexes = true
		fromUbuntu := isUbuntuArchive(entry.Name())
		for _, stanza := range readStanzas(file) {
			name := stanza["Package"]
			if name == "" {
				continue
			}
			if fromUbuntu {
				origins.inUbuntu[name] = true
			} else if !slices.Contains(origins.thirdParty[name], index) {
				origins.thirdParty[name] = append(origins.thirdParty[name], index)
			}
		}
		_ = file.Close() // read-only
	}
	return origins
}

// isUbuntuArchiveURI reports whether a source URI points at Ubuntu's archive.
func isUbuntuArchiveURI(uri string) bool {
	_, rest, found := strings.Cut(uri, "://")
	if !found {
		return false
	}
	host, _, _ := strings.Cut(rest, "/")
	return isUbuntuArchive(host + "_")
}

// relativePath returns path relative to the root with a leading slash, for
// reports; paths outside the root are returned unchanged.
func (root systemRoot) relativePath(path string) string {
	relative, err := filepath.Rel(string(root), path)
	if err != nil || strings.HasPrefix(relative, "..") {
		return path
	}
	return "/" + filepath.ToSlash(relative)
}
