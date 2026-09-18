package recipe

import (
	"bytes"
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// Lockfile is the content of frostroot.lock: what a build actually produced.
// It is written by build and committed next to the recipe.
type Lockfile struct {
	Version          int      `toml:"version"` // lock format version
	Distro           string   `toml:"distro"`
	Release          string   `toml:"release"`
	Suite            string   `toml:"suite"`
	Arch             string   `toml:"arch"`
	Mirror           string   `toml:"mirror"`  // archive URL actually used
	Sources          []string `toml:"sources"` // the three deb lines actually used
	FrostrootVersion string   `toml:"frostroot_version"`
	Requested        []string `toml:"requested"` // the recipe's packages.include, as written
	// SourceDateEpoch is the instant the image is frozen at, in seconds since
	// 1970: no file in the tarball is dated later. An offline build freezes at
	// the same instant, which is what makes two offline builds of one lock
	// byte-identical. Locks written before frostroot 0.6 have none.
	SourceDateEpoch int64 `toml:"source_date_epoch,omitempty"`
	// Repositories are the recipe's extra sources as the build used them,
	// with the checksum of each signing key file. Absent without sources.
	Repositories []LockRepository `toml:"repositories,omitempty"`
	// Certificates are the certificate authorities the recipe named, with
	// the checksum of each file as the build read it. Absent without
	// [certificates]. A build-time --ca-bundle is deliberately not here: it
	// changes nothing about the image, so it must not change the lock.
	Certificates []LockCertificate `toml:"certificates,omitempty"`
	// Python records the recipe's [python] table as the build used it, and
	// the tools that resolved the wheels. Absent without Python packages.
	Python   *LockPython   `toml:"python,omitempty"`
	Packages []LockPackage `toml:"packages"` // every installed package, sorted
	// PyPI is every package in the image's virtual environment, sorted, with
	// the wheel each came from. Absent without Python packages.
	PyPI []LockPyPI `toml:"pypi,omitempty"`
}

// LockPython is the Python side of a build: what the recipe asked for, where
// the virtual environment went, and which tools resolved the wheels. The
// versions matter for the same reason mmdebstrap's does: another pip may
// resolve another set.
type LockPython struct {
	Requested   []string `toml:"requested"`             // the recipe's python.include, as written
	Venv        string   `toml:"venv"`                  // absolute path of the virtual environment in the image
	Interpreter string   `toml:"interpreter"`           // the interpreter that created it, as it reports itself
	PipVersion  string   `toml:"pip_version,omitempty"` // the pip that resolved the wheels
}

// LockPyPI is one package in the image's virtual environment and the wheel it
// was installed from. The URL is absolute: PyPI serves files from a
// content-addressed host rather than from a base URL with a pool layout, so
// there is no mirror to join a relative name to.
type LockPyPI struct {
	Name    string `toml:"name"`    // the distribution name as PyPI spells it
	Version string `toml:"version"` // the version resolved, never a range
	// Auto marks a package the resolver pulled in to satisfy a dependency,
	// as opposed to one the recipe asked for by name. It is the same
	// distinction LockPackage.Auto records for apt.
	Auto     bool   `toml:"auto,omitempty"`
	SHA256   string `toml:"sha256"`         // hex digest of the wheel
	Size     int64  `toml:"size,omitempty"` // bytes of the wheel, when the resolver reported it
	Filename string `toml:"filename"`       // the wheel's file name, as vendor/wheels holds it
	URL      string `toml:"url"`            // where the wheel was downloaded from
}

// LockCertificate is one certificate authority the image trusts. Path is
// the recipe's own spelling, Name what the file is called in the image
// without its .crt, and SHA256 the digest of the file the build read, so
// that an offline rebuild refuses a certificate that changed underneath it.
type LockCertificate struct {
	Name   string `toml:"name"`
	Path   string `toml:"path"`
	SHA256 string `toml:"sha256"`
}

// LockRepository is one extra source the build installed from.
type LockRepository struct {
	Name       string   `toml:"name"`
	URL        string   `toml:"url"`
	Suite      string   `toml:"suite"`
	Components []string `toml:"components"`
	KeySHA256  string   `toml:"key_sha256"` // hex digest of the recipe's key file
}

// Repository returns the repository named name.
func (l Lockfile) Repository(name string) (LockRepository, bool) {
	for _, repository := range l.Repositories {
		if repository.Name == name {
			return repository, true
		}
	}
	return LockRepository{}, false
}

// LockPackage is one installed package, and where its .deb file comes from.
// SHA256, Size and Filename are recorded from the apt index the build
// installed from, so that vendor can fetch the very same file and check it.
// Locks written before frostroot 0.4 have none of the three.
type LockPackage struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Arch    string `toml:"arch"`
	// Auto marks a package apt installed on its own to satisfy a dependency,
	// as opposed to one the build asked for by name. An offline build
	// restores the marks, so that apt autoremove and frostroot capture see
	// the same image either way. Locks written before frostroot 0.6 have none.
	Auto     bool   `toml:"auto,omitempty"`
	SHA256   string `toml:"sha256,omitempty"`   // hex digest of the .deb file
	Size     int64  `toml:"size,omitempty"`     // bytes of the .deb file
	Filename string `toml:"filename,omitempty"` // path of the .deb below its source's base URL
	// Source names the repository Filename is relative to; empty for the
	// Ubuntu archive (Mirror).
	Source string `toml:"source,omitempty"`
}

// HasChecksums reports whether every package carries the checksum, size and
// file name vendoring needs. A lock written by frostroot 0.3 or earlier has
// none; one written since has all of them.
func (l Lockfile) HasChecksums() bool {
	if len(l.Packages) == 0 {
		return false
	}
	for _, locked := range l.Packages {
		if locked.SHA256 == "" || locked.Size <= 0 || locked.Filename == "" {
			return false
		}
	}
	return true
}

// HasWheelChecksums reports whether every Python package carries what
// vendoring needs: a checksum, a file name and the URL to fetch it from. A
// lock with no Python packages has nothing to vendor and reports true, so
// callers can check it before deciding to fetch.
func (l Lockfile) HasWheelChecksums() bool {
	for _, wheel := range l.PyPI {
		if wheel.SHA256 == "" || wheel.Filename == "" || wheel.URL == "" {
			return false
		}
	}
	return true
}

// LoadLock parses the lockfile at path. Unknown fields are an error.
func LoadLock(path string) (Lockfile, error) {
	var parsed Lockfile
	if err := decodeStrict(path, &parsed); err != nil {
		return Lockfile{}, err
	}
	return parsed, nil
}

// SaveLock writes lock to path. The output depends only on the input, and
// arrays are written one element per line, so a rebuild that changes one
// version shows up as a one-line diff. Callers sort lock.Packages.
func SaveLock(path string, lock Lockfile) error {
	var encoded bytes.Buffer
	encoder := toml.NewEncoder(&encoded)
	encoder.SetArraysMultiline(true)
	if err := encoder.Encode(lock); err != nil {
		return fmt.Errorf("encoding lock: %w", err)
	}
	return os.WriteFile(path, encoded.Bytes(), 0o644)
}
