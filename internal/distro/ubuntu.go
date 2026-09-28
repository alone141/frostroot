// Package distro knows the distribution families frostroot builds and their
// releases: for Ubuntu, the LTS releases, their suites, where their archives
// live, and which apt source lines to use; for Fedora, the releases, their
// repositories and the keys that sign them.
package distro

import (
	"errors"
	"fmt"
	"strings"
)

// Errors returned by Lookup. Compare with errors.Is.
var (
	// ErrUnknownRelease means the version is not a release frostroot supports.
	ErrUnknownRelease = errors.New("unknown ubuntu release")
	// ErrUnsupportedArch means the architecture is not SupportedArch.
	ErrUnsupportedArch = errors.New("unsupported arch")
)

// SupportedArch is the only architecture frostroot v1 builds.
const SupportedArch = "amd64"

// ubuntuArchiveURL serves every supported release. 20.04 is past standard
// support, but an LTS release under ESM is not moved to old-releases: every
// focal pocket returns 404 there (Task 0 spike, 2026-09-16).
const ubuntuArchiveURL = "http://archive.ubuntu.com/ubuntu"

// Release describes how to bootstrap one Ubuntu release.
type Release struct {
	Suite      string   // apt code name, such as "noble"
	ArchiveURL string   // base URL of the archive the three pockets come from
	Components []string // archive components to enable
	EndOfLife  bool     // past standard support: security fixes need Ubuntu Pro
	// ExternallyManagedPython means the release's python3 refuses pip
	// install outside a virtual environment (PEP 668), which someone who
	// asks for python3-pip is told.
	ExternallyManagedPython bool
}

// allComponents is the whole archive, in the order Ubuntu's own sources.list
// names it: what a stock install and the official WSL image enable. Up to
// v0.9 frostroot enabled main and universe only; a lock made then still
// rebuilds offline to the same bytes, because the lock records the image's
// deb lines and an offline build writes those.
var allComponents = []string{"main", "restricted", "universe", "multiverse"}

// releasesByVersion is every release frostroot builds. A release joins it
// once a spike has built it end to end; 26.04's is recorded in
// docs/superpowers/specs/2026-09-28-frostroot-resolute.md.
var releasesByVersion = map[string]Release{
	"20.04": {Suite: "focal", ArchiveURL: ubuntuArchiveURL, Components: allComponents, EndOfLife: true},
	"22.04": {Suite: "jammy", ArchiveURL: ubuntuArchiveURL, Components: allComponents},
	"24.04": {Suite: "noble", ArchiveURL: ubuntuArchiveURL, Components: allComponents, ExternallyManagedPython: true},
	"26.04": {Suite: "resolute", ArchiveURL: ubuntuArchiveURL, Components: allComponents, ExternallyManagedPython: true},
}

// Lookup returns how to bootstrap an Ubuntu release, such as "24.04", for an
// architecture. When both the version and the architecture are unsupported the
// error wraps ErrUnknownRelease and ErrUnsupportedArch together (errors.Join,
// release first), so a validator can report each.
func Lookup(version, arch string) (Release, error) {
	var problems []error
	release, known := releasesByVersion[version]
	if !known {
		problems = append(problems, fmt.Errorf("%w: %q (known: %s)", ErrUnknownRelease, version, strings.Join(SupportedVersions(Ubuntu), ", ")))
	}
	if arch != SupportedArch {
		problems = append(problems, fmt.Errorf("%w: %q (v1 supports %s only)", ErrUnsupportedArch, arch, SupportedArch))
	}
	if len(problems) > 0 {
		return Release{}, errors.Join(problems...)
	}
	// Copy the slice so callers cannot modify the shared table.
	release.Components = append([]string(nil), release.Components...)
	return release, nil
}

// SourceLines returns the three apt source lines for the release: the release
// pocket, -updates and -security. Without -updates and -security an image ships
// release-day packages and years of unpatched vulnerabilities.
//
// This is the only place deb lines are built. A non-empty mirrorURL, from
// build --mirror, replaces the archive URL in all three.
func (r Release) SourceLines(mirrorURL string) []string {
	archiveURL := r.ArchiveURL
	if mirrorURL != "" {
		archiveURL = mirrorURL
	}
	components := strings.Join(r.Components, " ")
	return []string{
		fmt.Sprintf("deb %s %s %s", archiveURL, r.Suite, components),
		fmt.Sprintf("deb %s %s-updates %s", archiveURL, r.Suite, components),
		fmt.Sprintf("deb %s %s-security %s", archiveURL, r.Suite, components),
	}
}

// SupportedVersions lists the versions of family that frostroot builds,
// oldest first, and none for a family it does not know.
func SupportedVersions(family Family) []string {
	switch family {
	case Ubuntu:
		return []string{"20.04", "22.04", "24.04", "26.04"}
	case Fedora:
		return []string{"44"}
	}
	return nil
}

// NewestVersion returns the newest release of family that frostroot builds,
// which a new recipe of that family starts on, or "" for a family it does
// not know.
func NewestVersion(family Family) string {
	versions := SupportedVersions(family)
	if len(versions) == 0 {
		return ""
	}
	return versions[len(versions)-1]
}

// VersionsInStandardSupport lists the releases of family that are not past
// their standard support, oldest first: the ones to prefer over a release
// that is.
func VersionsInStandardSupport(family Family) []string {
	switch family {
	case Ubuntu:
		var versions []string
		for _, version := range SupportedVersions(Ubuntu) {
			if !releasesByVersion[version].EndOfLife {
				versions = append(versions, version)
			}
		}
		return versions
	case Fedora:
		// A Fedora release leaves the table when its support ends: its
		// packages move to another server, so every one in it is supported.
		return SupportedVersions(Fedora)
	}
	return nil
}
