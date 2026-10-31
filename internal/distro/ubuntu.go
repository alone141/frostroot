// Package distro knows the distribution families frostroot builds and their
// releases: for Ubuntu, the LTS releases, their suites, where their archives
// live, and which apt source lines to use; for Fedora, the releases, their
// repositories and the keys that sign them.
package distro

import (
	"errors"
	"fmt"
	"slices"
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
	// DiskPackages are what a disk image of the release holds beyond a
	// WSL image: the kernel, the signed boot chain, and what the first
	// boot needs to grow the root and bring the network up. Nil for a
	// release frostroot makes no disk of: the bare-metal spec's spike
	// measured 26.04, and 24.04 differs from it in ways the spec read.
	DiskPackages []string
	// FirstBootSSHKeys means the release's openssh-server makes no host
	// keys at a first boot by itself, so a disk image that holds it ships
	// a unit that does. 26.04's sshd-keygen.service does it on its own.
	FirstBootSSHKeys bool
}

// ubuntuDiskPackages are what every disk image of Ubuntu holds beyond a
// WSL image's packages, on 24.04 and 26.04 alike: linux-image-generic
// brings the modules, the firmware and the microcode a real PC needs, where
// -virtual does not; initramfs-tools is named because 26.04 ships dracut
// too; the signed GRUB and shim boot under Secure Boot with Microsoft's
// keys; mokutil shows the state; dosfstools checks the EFI system
// partition; netplan with systemd-networkd and systemd-resolved bring the
// network up. Not grub-pc, which conflicts.
var ubuntuDiskPackages = []string{
	"linux-image-generic", "initramfs-tools", "grub-efi-amd64", "grub-efi-amd64-signed", "shim-signed",
	"mokutil", "dosfstools", "netplan.io", "systemd-resolved",
}

// resoluteDiskPackages adds systemd-repart, which grows the root partition
// at the first boot, and is a package of universe on 26.04 where 24.04's
// systemd carries it.
var resoluteDiskPackages = append(slices.Clone(ubuntuDiskPackages), "systemd-repart")

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
	"24.04": {Suite: "noble", ArchiveURL: ubuntuArchiveURL, Components: allComponents, ExternallyManagedPython: true, DiskPackages: ubuntuDiskPackages, FirstBootSSHKeys: true},
	"26.04": {Suite: "resolute", ArchiveURL: ubuntuArchiveURL, Components: allComponents, ExternallyManagedPython: true, DiskPackages: resoluteDiskPackages},
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
	// Copy the slices so callers cannot modify the shared table.
	release.Components = append([]string(nil), release.Components...)
	release.DiskPackages = slices.Clone(release.DiskPackages)
	return release, nil
}

// BuildsDisks reports whether frostroot makes disk images of the release.
func (r Release) BuildsDisks() bool {
	return r.DiskPackages != nil
}

// DiskVersions lists the versions of family that frostroot makes disk
// images of, oldest first, and none for a family whose disk is not built
// yet.
func DiskVersions(family Family) []string {
	var versions []string
	if family == Ubuntu {
		for _, version := range SupportedVersions(Ubuntu) {
			if releasesByVersion[version].BuildsDisks() {
				versions = append(versions, version)
			}
		}
	}
	return versions
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
