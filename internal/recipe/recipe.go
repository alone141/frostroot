// Package recipe owns frostroot's two files: frostroot.toml (intent, edited
// by people) and frostroot.lock (fact, written by build). Nothing else reads
// or writes them directly.
package recipe

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/pelletier/go-toml/v2"
)

// Recipe is the content of frostroot.toml. Package versions never appear
// here; they live in the lock.
type Recipe struct {
	Image        Image         `toml:"image"`
	User         User          `toml:"user"`
	WSL          WSL           `toml:"wsl"`
	Disk         *Disk         `toml:"disk,omitempty"`
	Locale       Locale        `toml:"locale"`
	Packages     Packages      `toml:"packages"`
	Sources      []Source      `toml:"sources,omitempty"`
	Python       *Python       `toml:"python,omitempty"`
	Certificates *Certificates `toml:"certificates,omitempty"`
}

// Source is one [[sources]] table: an apt repository besides the Ubuntu
// archive, such as a PPA or a vendor's repository, with the key that signs
// it. Every source has a suite and components; flat repositories and
// unsigned sources are not supported.
type Source struct {
	Name       string   `toml:"name"`                 // names the keyring in the image and the source in the lock
	URL        string   `toml:"url"`                  // base URL, such as https://download.docker.com/linux/ubuntu
	Suite      string   `toml:"suite,omitempty"`      // defaults to the release's code name
	Components []string `toml:"components,omitempty"` // defaults to ["main"]
	Key        string   `toml:"key"`                  // OpenPGP public key file, relative to the recipe directory
}

// DefaultComponents is what a source installs from when it names none.
var DefaultComponents = []string{"main"}

// SuiteFor returns the source's suite, or releaseSuite when it names none.
func (s Source) SuiteFor(releaseSuite string) string {
	if s.Suite != "" {
		return s.Suite
	}
	return releaseSuite
}

// ComponentsOrDefault returns the source's components, or DefaultComponents.
func (s Source) ComponentsOrDefault() []string {
	if len(s.Components) > 0 {
		return s.Components
	}
	return DefaultComponents
}

// Equal reports whether s and other are the same source. Nil and empty
// component lists are the same: TOML omitempty and a written empty array
// must not look like a hand edit.
func (s Source) Equal(other Source) bool {
	return s.Name == other.Name && s.URL == other.URL && s.Suite == other.Suite && s.Key == other.Key && slices.Equal(s.Components, other.Components)
}

// Image is the [image] table: which distribution and release to build and
// what to call the result.
type Image struct {
	Name string `toml:"name"` // tarball file name and WSL distribution name
	// Distro is the family, such as "ubuntu". Every recipe written before
	// there was a second family names none, and is Ubuntu.
	Distro  string `toml:"distro,omitempty"`
	Release string `toml:"release"` // the family's version, such as Ubuntu's "24.04"
	Arch    string `toml:"arch"`    // CPU architecture; only "amd64" in v1
	// Target is what the build makes: TargetWSL, a tarball for wsl --import,
	// or TargetDisk, a disk image that boots a PC or a VM. Every recipe
	// written before there was a second target names none, and is WSL's.
	Target string `toml:"target,omitempty"`
}

// The targets a recipe can name.
const (
	TargetWSL  = "wsl"
	TargetDisk = "disk"
)

// Target returns what the recipe builds: TargetWSL when it names nothing.
// Like PythonPackages, it is how the rest of frostroot reads the field, so a
// recipe without the line and one that says "wsl" behave the same.
func (r Recipe) Target() string {
	if r.Image.Target == "" {
		return TargetWSL
	}
	return r.Image.Target
}

// User is the [user] table: the account created in the image.
type User struct {
	Name string `toml:"name"`
	Sudo bool   `toml:"sudo"` // passwordless sudo when true
}

// WSL is the [wsl] table: settings written to /etc/wsl.conf.
type WSL struct {
	Systemd     bool   `toml:"systemd"`
	DefaultUser string `toml:"default_user,omitempty"` // defaults to User.Name
}

// Disk is the [disk] table: how a disk recipe's image is laid out and what
// it is written as. It is a pointer for the same reason [python] is: a WSL
// recipe has none, and is written without one.
type Disk struct {
	// Size is the image file's size, such as "16G" or "512M", in GiB or MiB.
	// Empty means the content and room to spare; either way the root grows
	// to fill the disk the image is written to, at its first boot.
	Size string `toml:"size,omitempty"`
	// Formats are what the image is written as besides the raw disk, which
	// is always written: "qcow2".
	Formats []string `toml:"formats,omitempty"`
}

// DiskFormatQcow2 is the one format [disk] formats may name so far, besides
// the raw image, which is always written.
const DiskFormatQcow2 = "qcow2"

// DiskFormats returns the formats the recipe asks for besides the raw
// image, or nil when it asks for none or has no [disk].
func (r Recipe) DiskFormats() []string {
	if r.Disk == nil {
		return nil
	}
	return r.Disk.Formats
}

// DiskSize returns the [disk] size as written, or "" when there is none.
func (r Recipe) DiskSize() string {
	if r.Disk == nil {
		return ""
	}
	return r.Disk.Size
}

// Locale is the [locale] table.
type Locale struct {
	Lang     string `toml:"lang,omitempty"`     // defaults to en_US.UTF-8
	Timezone string `toml:"timezone,omitempty"` // defaults to UTC
}

// Packages is the [packages] table.
type Packages struct {
	Include []string `toml:"include"` // apt package names, without versions
}

// Python is the [python] table: packages installed from PyPI into the
// image's virtual environment, after apt has installed everything else. As
// with apt packages, versions never appear here; the lock records the wheel
// each name resolved to. The table is a pointer so that a recipe without one
// stays as it was written, and is absent from a recipe Save writes.
type Python struct {
	Include []string `toml:"include"` // PyPI distribution names, without versions
	// IndexURL replaces PyPI as the index the packages resolve from, for a
	// network that mandates an internal mirror. Empty means PyPI.
	IndexURL string `toml:"index_url,omitempty"`
}

// PythonIndexURL returns the index the recipe resolves Python packages
// from, or "" for PyPI. Like PythonPackages, it is the only way the rest of
// frostroot reads the field, so an absent table behaves as an empty one.
func (r Recipe) PythonIndexURL() string {
	if r.Python == nil {
		return ""
	}
	return r.Python.IndexURL
}

// PythonPackages returns the PyPI names the recipe asks for, or nil when it
// asks for none. It is the only way the rest of frostroot reads [python], so
// an absent table and an empty list behave the same everywhere.
func (r Recipe) PythonPackages() []string {
	if r.Python == nil {
		return nil
	}
	return r.Python.Include
}

// Certificates is the [certificates] table: certificate authorities the
// image trusts, and that the build trusts while it fetches. Naming one is
// how a recipe says it is built inside an organization whose network
// inspects TLS. The files live beside the recipe, as a source's key does,
// and the lock records what each one hashed to. The table is a pointer for
// the same reason [python] is: a recipe without one stays as it was written.
type Certificates struct {
	Include []string `toml:"include"` // PEM files, relative to the recipe directory
}

// CertificatePaths returns the certificate files the recipe names, or nil
// when it names none. It is the only way the rest of frostroot reads
// [certificates], so an absent table and an empty list behave the same
// everywhere.
func (r Recipe) CertificatePaths() []string {
	if r.Certificates == nil {
		return nil
	}
	return r.Certificates.Include
}

// utf8ByteOrderMark is what editors such as Notepad write at the start of a
// file saved as "UTF-8 with BOM".
const utf8ByteOrderMark = "\xef\xbb\xbf"

// Load parses the recipe at path. Unknown fields are an error, so a typo such
// as [package] fails loudly instead of silently yielding an empty include list.
func Load(path string) (Recipe, error) {
	var parsed Recipe
	if err := decodeStrict(path, &parsed); err != nil {
		return Recipe{}, err
	}
	return parsed, nil
}

// Save writes imageRecipe to path without comments. The init command renders
// its own commented template instead; Save exists for round trips.
func Save(path string, imageRecipe Recipe) error {
	encoded, err := toml.Marshal(imageRecipe)
	if err != nil {
		return fmt.Errorf("encoding recipe: %w", err)
	}
	return os.WriteFile(path, encoded, 0o644)
}

// decodeStrict decodes the TOML file at path into target, rejecting unknown
// fields.
func decodeStrict(path string, target any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// TOML says nothing about byte order marks, and without this the error is
	// "invalid character at start of key: U+00EF", which explains nothing.
	content = bytes.TrimPrefix(content, []byte(utf8ByteOrderMark))
	decoder := toml.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return describeDecodeError(path, err)
	}
	return nil
}

// describeDecodeError turns go-toml errors into messages that point at the
// offending line, because they are read by people fixing a file.
func describeDecodeError(path string, err error) error {
	var unknownFieldErr *toml.StrictMissingError
	if errors.As(err, &unknownFieldErr) {
		return fmt.Errorf("%s: unknown field (check the spelling):\n%s", path, unknownFieldErr.String())
	}
	var syntaxErr *toml.DecodeError
	if errors.As(err, &syntaxErr) {
		line, column := syntaxErr.Position()
		return fmt.Errorf("%s:%d:%d: %w\n%s", path, line, column, syntaxErr, syntaxErr.String())
	}
	return fmt.Errorf("%s: %w", path, err)
}
