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

// Recipe is frostroot.toml. Versions never appear here; they live in the lock.
type Recipe struct {
	Image    Image    `toml:"image"`
	User     User     `toml:"user"`
	WSL      WSL      `toml:"wsl"`
	Locale   Locale   `toml:"locale"`
	Packages Packages `toml:"packages"`
}

type Image struct {
	Name    string `toml:"name"`
	Release string `toml:"release"`
	Arch    string `toml:"arch"`
}

type User struct {
	Name string `toml:"name"`
	Sudo bool   `toml:"sudo"` // passwordless sudo when true
}

type WSL struct {
	Systemd     bool   `toml:"systemd"`
	DefaultUser string `toml:"default_user,omitempty"` // defaults to User.Name
}

type Locale struct {
	Lang     string `toml:"lang,omitempty"`     // defaults to en_US.UTF-8
	Timezone string `toml:"timezone,omitempty"` // defaults to UTC
}

type Packages struct {
	Include []string `toml:"include"`
}

// Load parses a recipe. Unknown fields are an error, so a typo such as
// [package] fails loudly instead of silently yielding an empty include list.
func Load(path string) (Recipe, error) {
	var r Recipe
	if err := decodeStrict(path, &r); err != nil {
		return Recipe{}, err
	}
	return r, nil
}

// Save writes a recipe without comments. init renders its own commented
// template instead; Save exists for round trips.
func Save(path string, r Recipe) error {
	b, err := toml.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func decodeStrict(path string, v any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Notepad's "UTF-8 with BOM" is a real way for a recipe to be saved, and
	// TOML says nothing about byte order marks. Without this the error is
	// "invalid character at start of key: U+00EF", which explains nothing.
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	dec := toml.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return describe(path, err)
	}
	return nil
}

// describe turns go-toml errors into messages that point at the offending
// line, because validate's output is read by people fixing a file.
func describe(path string, err error) error {
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		return fmt.Errorf("%s: unknown field (check the spelling):\n%s", path, strict.String())
	}
	var de *toml.DecodeError
	if errors.As(err, &de) {
		row, col := de.Position()
		return fmt.Errorf("%s:%d:%d: %s\n%s", path, row, col, de.Error(), de.String())
	}
	return fmt.Errorf("%s: %w", path, err)
}
