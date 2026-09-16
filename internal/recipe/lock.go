package recipe

import (
	"bytes"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// Lockfile is frostroot.lock: what a build actually produced. It is written by
// build and committed next to the recipe.
type Lockfile struct {
	Version          int           `toml:"version"`
	Distro           string        `toml:"distro"`
	Release          string        `toml:"release"`
	Suite            string        `toml:"suite"`
	Arch             string        `toml:"arch"`
	Mirror           string        `toml:"mirror"`  // base URL actually used
	Sources          []string      `toml:"sources"` // the three deb lines actually used
	FrostrootVersion string        `toml:"frostroot_version"`
	Requested        []string      `toml:"requested"` // recipe packages.include as written
	Packages         []LockPackage `toml:"packages"`  // every installed package, sorted
}

// LockPackage is one installed package.
type LockPackage struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Arch    string `toml:"arch"`
	// sha256 and filename land here when vendoring arrives; the
	// list-of-tables shape exists so that stays additive.
}

// LoadLock parses a lockfile. Unknown fields are an error.
func LoadLock(path string) (Lockfile, error) {
	var l Lockfile
	if err := decodeStrict(path, &l); err != nil {
		return Lockfile{}, err
	}
	return l, nil
}

// SaveLock writes a lockfile. Output depends only on the input, and arrays are
// written one element per line, so a rebuild that changes one version shows
// up as a one-line diff. Callers sort Packages.
func SaveLock(path string, l Lockfile) error {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	enc.SetArraysMultiline(true)
	if err := enc.Encode(l); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
