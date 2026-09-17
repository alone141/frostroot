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
	Version          int           `toml:"version"` // lock format version
	Distro           string        `toml:"distro"`
	Release          string        `toml:"release"`
	Suite            string        `toml:"suite"`
	Arch             string        `toml:"arch"`
	Mirror           string        `toml:"mirror"`  // archive URL actually used
	Sources          []string      `toml:"sources"` // the three deb lines actually used
	FrostrootVersion string        `toml:"frostroot_version"`
	Requested        []string      `toml:"requested"` // the recipe's packages.include, as written
	Packages         []LockPackage `toml:"packages"`  // every installed package, sorted
}

// LockPackage is one installed package, and where its .deb file comes from.
// SHA256, Size and Filename are recorded from the apt index the build
// installed from, so that vendor can fetch the very same file and check it.
// Locks written before frostroot 0.4 have none of the three.
type LockPackage struct {
	Name     string `toml:"name"`
	Version  string `toml:"version"`
	Arch     string `toml:"arch"`
	SHA256   string `toml:"sha256,omitempty"`   // hex digest of the .deb file
	Size     int64  `toml:"size,omitempty"`     // bytes of the .deb file
	Filename string `toml:"filename,omitempty"` // path of the .deb below the mirror's base URL
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
