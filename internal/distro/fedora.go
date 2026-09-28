package distro

import (
	_ "embed" // the release keys are part of the table
	"errors"
	"fmt"
	"strings"
)

// Fedora is the family built with mkosi and Fedora's own dnf5, from
// Fedora's repositories.
const Fedora Family = "fedora"

// ErrUnknownFedoraRelease means the version is not a Fedora release
// frostroot builds.
var ErrUnknownFedoraRelease = errors.New("unknown fedora release")

// FedoraArch is how rpm names SupportedArch.
const FedoraArch = "x86_64"

// FedoraRelease describes how to build one Fedora release.
type FedoraRelease struct {
	Version string // such as "44"
	// Repositories are the release's own packages and its updates. A
	// package's repository is part of the image: dnf5 records where each
	// package came from, so an offline build installs from repositories of
	// the same names.
	Repositories []FedoraRepository
	// Key is the release's primary signing key, ASCII-armored, as Fedora
	// publishes it; frostroot carries it, so that no build fetches a key.
	// KeyFingerprint is the fingerprint fedoraproject.org/security gives
	// for it, and the only primary key Key may hold.
	Key            []byte
	KeyFingerprint string
}

// FedoraRepository is one repository a release installs from.
type FedoraRepository struct {
	ID string // the name dnf5 records a package's origin under, such as "updates"
	// Metalink is where an online build learns the mirrors, and the
	// checksum of repomd.xml, over HTTPS: repomd.xml itself is not signed.
	Metalink string
	// BaseURL is the repository on Fedora's own server, below which each
	// package's location is, for vendor and for a build's --mirror.
	BaseURL string
}

//go:embed keys/RPM-GPG-KEY-fedora-44-primary
var fedora44Key []byte

// fedoraReleasesByVersion is every Fedora release frostroot builds. A
// release joins it once a spike has built it end to end; 44's is recorded
// in docs/superpowers/specs/2026-09-28-frostroot-fedora.md.
var fedoraReleasesByVersion = map[string]FedoraRelease{
	"44": {
		Version: "44",
		Repositories: []FedoraRepository{
			{
				ID:       "fedora",
				Metalink: "https://mirrors.fedoraproject.org/metalink?repo=fedora-44&arch=" + FedoraArch,
				BaseURL:  "https://dl.fedoraproject.org/pub/fedora/linux/releases/44/Everything/" + FedoraArch + "/os",
			},
			{
				ID:       "updates",
				Metalink: "https://mirrors.fedoraproject.org/metalink?repo=updates-released-f44&arch=" + FedoraArch,
				BaseURL:  "https://dl.fedoraproject.org/pub/fedora/linux/updates/44/Everything/" + FedoraArch,
			},
		},
		Key:            fedora44Key,
		KeyFingerprint: "36F612DCF27F7D1A48A835E4DBFCF71C6D9F90A6",
	},
}

// LookupFedora returns how to build a Fedora release, such as "44", for an
// architecture. When both are wrong the error joins ErrUnknownFedoraRelease
// and ErrUnsupportedArch, release first, as Lookup's does.
func LookupFedora(version, arch string) (FedoraRelease, error) {
	var problems []error
	release, known := fedoraReleasesByVersion[version]
	if !known {
		problems = append(problems, fmt.Errorf("%w: %q (known: %s)", ErrUnknownFedoraRelease, version, strings.Join(SupportedVersions(Fedora), ", ")))
	}
	if arch != SupportedArch {
		problems = append(problems, fmt.Errorf("%w: %q (v1 supports %s only)", ErrUnsupportedArch, arch, SupportedArch))
	}
	if len(problems) > 0 {
		return FedoraRelease{}, errors.Join(problems...)
	}
	return release, nil
}
