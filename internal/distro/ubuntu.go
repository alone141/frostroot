// Package distro knows the Ubuntu LTS releases frostroot can bootstrap: their
// suites, where their archives live, and which apt source lines to use.
package distro

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrUnknownRelease  = errors.New("unknown ubuntu release")
	ErrUnsupportedArch = errors.New("unsupported arch")
)

// Info describes how to bootstrap one release.
type Info struct {
	Suite      string
	Base       string // archive base URL, e.g. http://archive.ubuntu.com/ubuntu
	Components []string
	EOL        bool // past standard support: security fixes need Ubuntu Pro
}

const archive = "http://archive.ubuntu.com/ubuntu"

// All three releases are served from the archive. 20.04 is past standard
// support, but an LTS release under ESM is not moved to old-releases: every
// focal pocket 404s there (Task 0 spike, 2026-09-16).
var table = map[string]Info{
	"20.04": {Suite: "focal", Base: archive, Components: []string{"main", "universe"}, EOL: true},
	"22.04": {Suite: "jammy", Base: archive, Components: []string{"main", "universe"}},
	"24.04": {Suite: "noble", Base: archive, Components: []string{"main", "universe"}},
}

// Lookup returns bootstrap information for an Ubuntu release. v1 supports
// amd64 only.
func Lookup(release, arch string) (Info, error) {
	if arch != "amd64" {
		return Info{}, fmt.Errorf("%w: %q (v1 supports amd64 only)", ErrUnsupportedArch, arch)
	}
	info, ok := table[release]
	if !ok {
		return Info{}, fmt.Errorf("%w: %q (known: %s)", ErrUnknownRelease, release, strings.Join(KnownReleases(), ", "))
	}
	out := info
	out.Components = append([]string{}, info.Components...)
	return out, nil
}

// Sources returns the three apt source lines for this release: the release
// pocket, -updates and -security. Without -updates and -security the image
// ships release-day packages and therefore years of unpatched CVEs.
//
// This is the only place deb lines are constructed. A non-empty baseOverride
// (build --mirror) replaces the base URL in all three.
func (i Info) Sources(baseOverride string) []string {
	base := i.Base
	if baseOverride != "" {
		base = baseOverride
	}
	comps := strings.Join(i.Components, " ")
	return []string{
		fmt.Sprintf("deb %s %s %s", base, i.Suite, comps),
		fmt.Sprintf("deb %s %s-updates %s", base, i.Suite, comps),
		fmt.Sprintf("deb %s %s-security %s", base, i.Suite, comps),
	}
}

// KnownReleases lists the supported releases, oldest first.
func KnownReleases() []string { return []string{"20.04", "22.04", "24.04"} }
