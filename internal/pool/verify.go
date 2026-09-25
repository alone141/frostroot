package pool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"frostroot/internal/deb"
)

// Status is what Verify found in a pool directory.
type Status struct {
	Present []Entry // files there with the right size and checksum
	Missing []Entry // files not there
	Corrupt []Entry // files there with another size or checksum
	// Extra is the files there that the lock does not name, sorted: .deb and
	// .whl packages, and the .tmp files of downloads that were cut short.
	Extra []string
}

// Complete reports whether every entry is present and correct. Extra files
// do not count against completeness.
func (s Status) Complete() bool { return len(s.Missing) == 0 && len(s.Corrupt) == 0 }

// Describe summarizes what is wrong, naming up to limit files, for an error
// message. It returns "" when the pool is complete.
func (s Status) Describe(limit int) string {
	var problems []string
	for _, entry := range s.Missing {
		problems = append(problems, entry.FileName+" (missing)")
	}
	for _, entry := range s.Corrupt {
		problems = append(problems, entry.FileName+" (wrong size or checksum)")
	}
	if len(problems) == 0 {
		return ""
	}
	sort.Strings(problems)
	if len(problems) > limit {
		problems = append(problems[:limit], fmt.Sprintf("and %d more", len(problems)-limit))
	}
	return strings.Join(problems, ", ")
}

// Verify checks dir against entries by size and SHA-256, reporting every
// file's state to onChecked as it goes (checked files so far, total). A
// directory that does not exist is a pool in which everything is missing.
func Verify(dir string, entries []Entry, onChecked func(checked, total int)) (Status, error) {
	var status Status
	listed := map[string]bool{}
	for index, entry := range entries {
		listed[entry.FileName] = true
		state, err := checkFile(filepath.Join(dir, entry.FileName), entry)
		if err != nil {
			return Status{}, err
		}
		switch state {
		case filePresent:
			status.Present = append(status.Present, entry)
		case fileMissing:
			status.Missing = append(status.Missing, entry)
		case fileCorrupt:
			status.Corrupt = append(status.Corrupt, entry)
		}
		if onChecked != nil {
			onChecked(index+1, len(entries))
		}
	}
	directoryEntries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	for _, directoryEntry := range directoryEntries {
		name := directoryEntry.Name()
		if !directoryEntry.IsDir() && (isPackageFile(name) || isPartialDownload(name)) && !listed[name] {
			status.Extra = append(status.Extra, name)
		}
	}
	sort.Strings(status.Extra)
	return status, nil
}

// fileState is what checkFile found.
type fileState int

const (
	filePresent fileState = iota
	fileMissing
	fileCorrupt
)

// checkFile compares the file at path with entry. A size mismatch is decided
// without reading the file.
func checkFile(path string, entry Entry) (fileState, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileMissing, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || (entry.Size > 0 && info.Size() != entry.Size) {
		return fileCorrupt, nil
	}
	_, digest, err := deb.SHA256File(path)
	if err != nil {
		return 0, err
	}
	if digest != entry.SHA256 {
		return fileCorrupt, nil
	}
	return filePresent, nil
}

// isPackageFile reports whether name is a package file, .deb or .whl.
func isPackageFile(name string) bool {
	return strings.HasSuffix(name, ".deb") || strings.HasSuffix(name, ".whl")
}

// isPartialDownload reports whether name is a download of Fetch's that was
// cut short: a hidden .tmp file, the shape export.CreateTemp gives them. A
// download removes its own when it fails or is canceled, but a vendor run
// that is abandoned at the second Ctrl-C, or killed, exits before that
// runs, so these count among what the lock does not name and Prune removes.
func isPartialDownload(name string) bool {
	return strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".tmp")
}

// Prune removes the files in dir that entries do not name, .deb or .whl
// packages and the .tmp files of downloads that were cut short, and returns
// their names, sorted. Nothing else in the directory is touched.
func Prune(dir string, entries []Entry) ([]string, error) {
	status, err := Verify(dir, entries, nil)
	if err != nil {
		return nil, err
	}
	for _, extra := range status.Extra {
		if err := os.Remove(filepath.Join(dir, extra)); err != nil {
			return nil, fmt.Errorf("removing %s: %w", extra, err)
		}
	}
	return status.Extra, nil
}
