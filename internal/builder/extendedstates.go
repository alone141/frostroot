package builder

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"frostroot/internal/deb"
	"frostroot/internal/recipe"
)

// aptExtendedStatesPath is apt's record, inside the image, of the packages it
// installed on its own to satisfy dependencies: what apt autoremove may
// remove, and what frostroot capture lists as pulled in rather than asked for.
const aptExtendedStatesPath = "/var/lib/apt/extended_states"

// AutoMark is one entry of apt's extended_states: a package marked as
// installed automatically. Arch is apt's own view, in which a package of
// architecture "all" carries the image's native architecture.
type AutoMark struct {
	Name string
	Arch string
}

// ParseExtendedStates reads an extended_states file and returns the packages
// it marks auto-installed, in file order. apt writes one paragraph per package
// it has ever marked, with Auto-Installed: 1 or, once the mark is taken back,
// 0.
func ParseExtendedStates(reader io.Reader) ([]AutoMark, error) {
	var marks []AutoMark
	err := deb.ReadStanzas(reader, func(stanza deb.Stanza) error {
		if stanza["Auto-Installed"] != "1" {
			return nil
		}
		mark := AutoMark{Name: stanza["Package"], Arch: stanza["Architecture"]}
		if mark.Name == "" || mark.Arch == "" {
			return errors.New("a paragraph lacks Package or Architecture")
		}
		marks = append(marks, mark)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("apt extended_states: %w", err)
	}
	return marks, nil
}

// RenderExtendedStates renders the extended_states of an image holding
// packages, in apt's format: one paragraph per package marked Auto, in the
// order given, with nativeArch standing in for "all" as apt writes it. No
// marks render as "", and apt itself writes the file only once it has marked
// something.
func RenderExtendedStates(packages []recipe.LockPackage, nativeArch string) string {
	var rendered strings.Builder
	for _, locked := range packages {
		if locked.Auto {
			fmt.Fprintf(&rendered, "Package: %s\nArchitecture: %s\nAuto-Installed: 1\n\n", locked.Name, aptArchitecture(locked.Arch, nativeArch))
		}
	}
	return rendered.String()
}

// aptArchitecture is the architecture apt records for a package of arch in
// an image whose native architecture is nativeArch: "all" packages count as
// native.
func aptArchitecture(arch, nativeArch string) string {
	if arch == "all" {
		return nativeArch
	}
	return arch
}

// markAutoInstalled sets Auto on every installed package that marks name. A
// mark naming no installed package is left alone; apt keeps the marks of
// packages removed since.
func markAutoInstalled(installed []recipe.LockPackage, marks []AutoMark, nativeArch string) {
	marked := make(map[AutoMark]bool, len(marks))
	for _, mark := range marks {
		marked[mark] = true
	}
	for index, installedPackage := range installed {
		installed[index].Auto = marked[AutoMark{Name: installedPackage.Name, Arch: aptArchitecture(installedPackage.Arch, nativeArch)}]
	}
}

// readExtendedStates parses the extended_states the bootstrap downloaded.
func readExtendedStates(path string) ([]AutoMark, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("bootstrap did not produce the image's apt extended_states: %w", err)
	}
	defer func() { _ = file.Close() }() // read-only: closing cannot lose data
	return ParseExtendedStates(file)
}
