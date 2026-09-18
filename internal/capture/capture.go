// Package capture reads an installed Ubuntu system and describes it as a
// recipe plus a report of what a recipe cannot carry. It reads package
// metadata and a few configuration files; the only files it copies are the
// public signing keys of apt sources, it never reads the content of a home
// directory beyond entry names, and it never needs root.
package capture

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"frostroot/internal/builder"
	"frostroot/internal/distro"
	"frostroot/internal/recipe"
	"frostroot/internal/sources"
)

// Errors for systems capture cannot describe. Compare with errors.Is.
var (
	// ErrNotLinuxRoot means the directory has no /etc/os-release or dpkg status.
	ErrNotLinuxRoot = errors.New("not the root of an installed Linux system")
	// ErrNotUbuntu means the system is another distribution.
	ErrNotUbuntu = errors.New("not an Ubuntu system")
	// ErrUnsupportedRelease means frostroot cannot build this Ubuntu release.
	ErrUnsupportedRelease = errors.New("unsupported Ubuntu release")
	// ErrUnsupportedArch means the system is not amd64.
	ErrUnsupportedArch = errors.New("unsupported architecture")
)

// fallbackImageName names the image when the hostname is unusable.
const fallbackImageName = "captured"

// Defaults when the system does not say.
const (
	fallbackUserName = "student"
	fallbackLocale   = "C.UTF-8"
	fallbackTimezone = "UTC"
)

// Snapshot is what capture found: the values a recipe can hold, where each
// came from, and the findings a recipe cannot hold.
type Snapshot struct {
	Root      string // the root that was read
	ImageName string
	Release   string
	Arch      string
	UserName  string
	Sudo      bool
	Systemd   bool
	Locale    string
	Timezone  string
	Packages  []string // packages asked for, sorted
	// Sources are the machine's third-party apt sources the recipe can hold,
	// and Keys their signing keys, armored, by source name.
	Sources []recipe.Source
	Keys    map[string][]byte
	// Certificates are the authorities the machine added under
	// /usr/local/share/ca-certificates, by the recipe-relative path the
	// recipe names them at. The caller writes them beside the recipe.
	Certificates map[string][]byte

	InstalledCount int // packages installed, for the report
	// Evidence says, one line each, where every captured value came from.
	Evidence []string
	// Findings lists every report area, in report order.
	Findings []Finding
}

// Recipe returns the recipe the snapshot describes. The caller validates it,
// as with every recipe about to be written.
func (s Snapshot) Recipe() recipe.Recipe {
	packages := slices.Clone(s.Packages)
	if packages == nil {
		packages = []string{}
	}
	imageRecipe := recipe.Recipe{
		Image:    recipe.Image{Name: s.ImageName, Release: s.Release, Arch: s.Arch},
		User:     recipe.User{Name: s.UserName, Sudo: s.Sudo},
		WSL:      recipe.WSL{Systemd: s.Systemd, DefaultUser: s.UserName},
		Locale:   recipe.Locale{Lang: s.Locale, Timezone: s.Timezone},
		Packages: recipe.Packages{Include: packages},
		Sources:  slices.Clone(s.Sources),
	}
	if paths := s.certificatePaths(); len(paths) > 0 {
		imageRecipe.Certificates = &recipe.Certificates{Include: paths}
	}
	return imageRecipe
}

// Read describes the system installed under rootDir ("/" for the running
// one). It fails only when no recipe can be written: the directory is not an
// installed Linux system, not Ubuntu, an Ubuntu release frostroot cannot
// build, or not amd64. Anything else it cannot read becomes a line in the
// report.
func Read(rootDir string) (Snapshot, error) {
	root := systemRoot(filepath.Clean(rootDir))
	snapshot := Snapshot{Root: string(root)}

	release, suite, err := root.checkRelease()
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Release = release

	installed, ok := root.installedPackages()
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: no dpkg status under %s", ErrNotLinuxRoot, root)
	}
	arch := dpkgArchitecture(installed)
	if arch != distro.SupportedArch {
		return Snapshot{}, fmt.Errorf("%w: %q; frostroot builds %s images", ErrUnsupportedArch, arch, distro.SupportedArch)
	}
	snapshot.Arch = arch
	snapshot.InstalledCount = len(installed)

	auto, haveAutoMarks := root.autoInstalled()
	requested, dropped := requestedPackages(installed, auto)
	snapshot.Packages = requested
	switch {
	case haveAutoMarks:
		snapshot.note("%d packages asked for, of %d installed (apt's automatic marks in /%s)", len(requested), len(installed), extendedStatesPath)
	default:
		snapshot.note("%d packages listed, of %d installed: apt kept no record of which were automatic, so every non-base package is included; the recipe rebuilds the same set but is verbose", len(requested), len(installed))
	}
	if len(dropped) > 0 {
		snapshot.note("dropped %d installed names that are not valid apt package names: %v", len(dropped), dropped)
	}

	snapshot.readIdentity(root)
	accounts := root.accounts()
	homeDir, groups := snapshot.readUser(root, accounts)
	snapshot.readLocaleAndTimezone(root)

	carried, left := root.sourcesForRecipe(suite)
	// The recipe vouches for one repository, not for every repository on its
	// host: a carried PPA must not cover the other PPAs on Launchpad.
	carriedIndexes := map[aptIndex]bool{}
	for _, source := range carried {
		snapshot.Sources = append(snapshot.Sources, source.source)
		if snapshot.Keys == nil {
			snapshot.Keys = map[string][]byte{}
		}
		snapshot.Keys[source.source.Name] = source.key
		carriedIndexes[aptIndex{prefix: builder.AptListPrefix(source.source.URL), suite: source.source.SuiteFor(suite)}] = true
		snapshot.note("source %q (%s) from %s", source.source.Name, sources.Describe(source.source), source.from)
	}

	carriedCertificates, leftCertificates := root.certificatesForRecipe()
	for _, certificate := range carriedCertificates {
		if snapshot.Certificates == nil {
			snapshot.Certificates = map[string][]byte{}
		}
		snapshot.Certificates[certificate.path] = certificate.pem
		snapshot.note("certificate %q from %s", certificate.path, certificate.from)
	}

	owned, haveOwnership := root.ownedPaths()
	origins := root.packageOrigins()
	thirdParty, unsourced := thirdPartyPackageFindings(requested, origins, carriedIndexes)
	unaccounted := root.unaccountedTrustFinding(owned, haveOwnership, carriedCertificates)
	for _, certificate := range leftCertificates {
		unaccounted.Examples = append(unaccounted.Examples, certificate.String())
		unaccounted.Count++
	}
	snapshot.Findings = []Finding{
		thirdPartySourceFinding(left),
		unaccounted,
		thirdParty,
		unsourced,
		modifiedConfigFinding(root.modifiedConffiles(installed)),
		root.addedEtcFinding(owned, haveOwnership),
		root.outsideAptFinding(homeDir),
		root.servicesFinding(owned, haveOwnership),
		otherUsersFinding(accounts, snapshot.UserName),
		root.homeFinding(homeDir),
		groupsFinding(groups, snapshot.UserName),
	}
	return snapshot, nil
}

// note records where a captured value came from.
func (s *Snapshot) note(format string, args ...any) {
	s.Evidence = append(s.Evidence, fmt.Sprintf(format, args...))
}

// checkRelease reads /etc/os-release and returns the Ubuntu version and its
// code name when frostroot can build it.
func (root systemRoot) checkRelease() (version, suite string, err error) {
	release, ok := root.osRelease()
	if !ok {
		return "", "", fmt.Errorf("%w: no readable /etc/os-release under %s", ErrNotLinuxRoot, root)
	}
	if release.id != "ubuntu" {
		return "", "", fmt.Errorf("%w: /etc/os-release says ID=%s", ErrNotUbuntu, release.id)
	}
	known, err := distro.Lookup(release.version, distro.SupportedArch)
	if err != nil {
		return "", "", fmt.Errorf("%w: Ubuntu %s; frostroot builds %v", ErrUnsupportedRelease, release.version, distro.SupportedVersions())
	}
	return release.version, known.Suite, nil
}

// readIdentity sets the image name from the hostname.
func (s *Snapshot) readIdentity(root systemRoot) {
	hostname, ok := root.readText(hostnamePath)
	s.ImageName = imageNameFromHostname(hostname, fallbackImageName)
	switch {
	case !ok:
		s.note("image name %q: no /etc/hostname to take it from", s.ImageName)
	case s.ImageName == fallbackImageName:
		s.note("image name %q: the hostname %q is not usable as one", s.ImageName, hostname)
	default:
		s.note("image name %q from /etc/hostname", s.ImageName)
	}
}

// readUser chooses the user, decides sudo and systemd, and returns the
// user's home directory (empty when none is known) and groups.
func (s *Snapshot) readUser(root systemRoot, accounts []account) (homeDir string, groups []string) {
	wsl := root.wslConf()
	s.Systemd = wsl.systemd
	if wsl.present {
		s.note("systemd %v from /etc/wsl.conf", s.Systemd)
	} else {
		s.note("systemd on: no /etc/wsl.conf to say otherwise")
	}

	var chosen *account
	switch {
	case wsl.defaultUser != "" && recipe.CheckUserName(wsl.defaultUser) == nil:
		s.UserName = wsl.defaultUser
		s.note("user %q from /etc/wsl.conf", s.UserName)
		for index := range accounts {
			if accounts[index].name == s.UserName {
				chosen = &accounts[index]
			}
		}
	case len(accounts) > 0 && recipe.CheckUserName(accounts[0].name) == nil:
		chosen = &accounts[0]
		s.UserName = chosen.name
		s.note("user %q: the first account in /etc/passwd with uid %d or above", s.UserName, firstHumanUID)
	default:
		s.UserName = fallbackUserName
		s.note("user %q: no account found to take it from", s.UserName)
	}

	primaryGID := -1
	if chosen != nil {
		primaryGID = chosen.gid
		// The home directory comes out of the captured passwd file, so with
		// --root DIR it is a path that tree chose. Only entry names are ever
		// read from it, but they should be that tree's names and not this
		// machine's.
		if resolved, inside := root.pathInRoot(chosen.home); inside {
			homeDir = resolved
		} else {
			s.note("home directory %q is outside %s, so it was not read", chosen.home, root)
		}
	}
	groups = root.groupsOf(s.UserName, primaryGID)
	hasSudo, evidence := root.sudoEvidence(s.UserName, groups)
	s.Sudo = hasSudo
	if hasSudo {
		s.note("passwordless sudo: the user can sudo (%s); whether a password is asked is not recorded, so the recipe grants it without one", evidence)
	} else {
		s.note("no sudo: the user is in neither the sudo nor the admin group and no readable sudoers drop-in names them")
	}
	return homeDir, groups
}

// readLocaleAndTimezone sets the locale and timezone with their fallbacks.
func (s *Snapshot) readLocaleAndTimezone(root systemRoot) {
	if lang, found := root.locale(); found {
		s.Locale = lang
		s.note("locale %q from /etc/default/locale", lang)
	} else {
		s.Locale = fallbackLocale
		s.note("locale %q: /etc/default/locale has no usable LANG", fallbackLocale)
	}
	if zone, source, found := root.timezone(); found {
		s.Timezone = zone
		s.note("timezone %q from %s", zone, source)
	} else {
		s.Timezone = fallbackTimezone
		s.note("timezone %q: neither /etc/timezone nor the /etc/localtime link names a zone", fallbackTimezone)
	}
}
