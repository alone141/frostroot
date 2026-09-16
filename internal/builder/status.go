package builder

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"frostroot/internal/recipe"
)

// maxDpkgStatusLineBytes bounds a single line of the status file. Depends and
// Description fields can be long.
const maxDpkgStatusLineBytes = 4 << 20

// ParseDpkgStatus reads a dpkg status file (/var/lib/dpkg/status, downloaded
// out of the image by a hook) and returns every installed package, sorted by
// name and then architecture.
//
// Parsing the file in Go means no host tool is involved: dpkg-query --root
// needs dpkg 1.21 or later on the host, which Ubuntu 20.04 and Debian 11 lack.
//
// A package counts when the third word of its Status field is "installed",
// whatever the selection state: "hold ok installed" is as present in the image
// as "install ok installed". Removed packages that left configuration files
// behind are listed in the file too, and are skipped.
func ParseDpkgStatus(statusFile io.Reader) ([]recipe.LockPackage, error) {
	var installedPackages []recipe.LockPackage
	var incompletePackageNames []string
	var currentStanza map[string]string // field name to value

	finishStanza := func() {
		if currentStanza == nil {
			return
		}
		defer func() { currentStanza = nil }()
		statusWords := strings.Fields(currentStanza["Status"])
		if len(statusWords) != 3 || statusWords[2] != "installed" {
			return
		}
		installed := recipe.LockPackage{
			Name:    currentStanza["Package"],
			Version: currentStanza["Version"],
			Arch:    currentStanza["Architecture"],
		}
		if installed.Name == "" || installed.Version == "" || installed.Arch == "" {
			incompletePackageNames = append(incompletePackageNames, fmt.Sprintf("%q", installed.Name))
			return
		}
		installedPackages = append(installedPackages, installed)
	}

	scanner := bufio.NewScanner(statusFile)
	scanner.Buffer(make([]byte, 0, 64<<10), maxDpkgStatusLineBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			finishStanza() // a blank line ends a stanza
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue // continuation of a multi-line field
		}
		fieldName, fieldValue, isField := strings.Cut(line, ":")
		if !isField {
			continue
		}
		if currentStanza == nil {
			currentStanza = map[string]string{}
		}
		currentStanza[fieldName] = strings.TrimSpace(fieldValue)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading dpkg status: %w", err)
	}
	finishStanza() // the last stanza need not end with a blank line

	if len(incompletePackageNames) > 0 {
		return nil, fmt.Errorf("dpkg status: installed package(s) missing Package, Version or Architecture: %s", strings.Join(incompletePackageNames, ", "))
	}
	if len(installedPackages) == 0 {
		return nil, errors.New("no installed packages found in dpkg status")
	}
	slices.SortFunc(installedPackages, func(left, right recipe.LockPackage) int {
		return cmp.Or(strings.Compare(left.Name, right.Name), strings.Compare(left.Arch, right.Arch))
	})
	return installedPackages, nil
}
