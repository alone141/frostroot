// Package recipe owns frostroot's two files: frostroot.toml (intent, edited
// by people) and frostroot.lock (fact, written by build). Nothing else reads
// or writes them directly.
package recipe

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// Recipe is the content of frostroot.toml. Package versions never appear
// here; they live in the lock.
type Recipe struct {
	Image    Image    `toml:"image"`
	User     User     `toml:"user"`
	WSL      WSL      `toml:"wsl"`
	Locale   Locale   `toml:"locale"`
	Packages Packages `toml:"packages"`
}

// Image is the [image] table: which Ubuntu release to build and what to call
// the result.
type Image struct {
	Name    string `toml:"name"`    // tarball file name and WSL distribution name
	Release string `toml:"release"` // Ubuntu version, such as "24.04"
	Arch    string `toml:"arch"`    // CPU architecture; only "amd64" in v1
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

// Locale is the [locale] table.
type Locale struct {
	Lang     string `toml:"lang,omitempty"`     // defaults to en_US.UTF-8
	Timezone string `toml:"timezone,omitempty"` // defaults to UTC
}

// Packages is the [packages] table.
type Packages struct {
	Include []string `toml:"include"` // apt package names, without versions
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
