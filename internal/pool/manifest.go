// Package pool keeps the vendored .deb files a lock names: it derives from
// the lock what should be in vendor/debs/, checks what is, downloads what is
// missing, and turns the files into the flat repository an offline build
// installs from. It reports progress through small callbacks rather than the
// builder's events, because the builder imports it.
package pool

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"frostroot/internal/recipe"
	"frostroot/internal/sources"
)

// DebsDirName is where the vendored packages live, relative to the recipe
// directory.
const DebsDirName = "vendor/debs"

// WheelsDirName is where the vendored Python wheels live, relative to the
// recipe directory.
const WheelsDirName = "vendor/wheels"

// Errors a caller can act on. Compare with errors.Is.
var (
	// ErrNoChecksums means the lock predates frostroot 0.4 and records no
	// checksums, so nothing can be vendored or verified from it.
	ErrNoChecksums = errors.New("frostroot.lock records no checksums")
	// ErrBadLock means the lock is not one this version understands.
	ErrBadLock = errors.New("unusable frostroot.lock")
)

// Entry is one vendored file: what the lock says about it and where it
// lives.
type Entry struct {
	Package  string // apt package name, or the PyPI distribution name
	Version  string
	Arch     string
	FileName string // base name in the pool directory, from the lock's filename
	URLPath  string // the lock's filename: the path below BaseURL
	BaseURL  string // the archive's mirror, or the extra source's URL
	Source   string // the lock's source name; "" for the archive
	// URL is the whole address of the file, used instead of BaseURL and
	// URLPath when it is set. A wheel has one: PyPI serves files from a
	// content-addressed path, so there is no base URL to join a name to.
	URL string
	// Size is the file's length, or 0 when the lock does not know it. pip's
	// installation report gives no size, so a wheel is checked by its
	// checksum alone.
	Size   int64
	SHA256 string // lowercase hex
}

// DownloadURL returns where the entry is fetched from.
func (e Entry) DownloadURL() string {
	if e.URL != "" {
		return e.URL
	}
	return strings.TrimRight(e.BaseURL, "/") + "/" + e.URLPath
}

// Manifest lists what a complete pool for lock holds, in the lock's order.
// It refuses locks of another format version, locks without checksums,
// packages from a repository the lock does not describe, and file names
// that could escape the pool directory.
func Manifest(lock recipe.Lockfile) ([]Entry, error) {
	if lock.Version != 1 {
		return nil, fmt.Errorf("%w: format version %d, want 1", ErrBadLock, lock.Version)
	}
	if !lock.HasChecksums() {
		return nil, ErrNoChecksums
	}
	entries := make([]Entry, 0, len(lock.Packages))
	seenFileNames := map[string]string{}
	for _, locked := range lock.Packages {
		fileName, err := poolFileName(locked.Filename)
		if err != nil {
			return nil, fmt.Errorf("%w: package %s: %w", ErrBadLock, locked.Name, err)
		}
		if other, seen := seenFileNames[fileName]; seen {
			return nil, fmt.Errorf("%w: packages %s and %s share the file name %s", ErrBadLock, other, locked.Name, fileName)
		}
		seenFileNames[fileName] = locked.Name
		baseURL := lock.Mirror
		if locked.Source != "" {
			repository, found := lock.Repository(locked.Source)
			if !found {
				return nil, fmt.Errorf("%w: package %s comes from source %q, which the lock does not describe", ErrBadLock, locked.Name, locked.Source)
			}
			baseURL = repository.URL
		}
		entries = append(entries, Entry{
			Package:  locked.Name,
			Version:  locked.Version,
			Arch:     locked.Arch,
			FileName: fileName,
			URLPath:  locked.Filename,
			BaseURL:  baseURL,
			Source:   locked.Source,
			Size:     locked.Size,
			SHA256:   strings.ToLower(locked.SHA256),
		})
	}
	return entries, nil
}

// WheelManifest lists what a complete wheel pool for lock holds, in the
// lock's order: every Python package the image's virtual environment was
// installed from. It refuses locks of another format version, wheels without
// what vendoring needs, file names that could escape the pool directory, and
// anything but an https URL, since the whole address comes from the lock.
func WheelManifest(lock recipe.Lockfile) ([]Entry, error) {
	if lock.Version != 1 {
		return nil, fmt.Errorf("%w: format version %d, want 1", ErrBadLock, lock.Version)
	}
	if len(lock.PyPI) == 0 {
		return nil, nil
	}
	if !lock.HasWheelChecksums() {
		return nil, ErrNoChecksums
	}
	entries := make([]Entry, 0, len(lock.PyPI))
	seenFileNames := map[string]string{}
	for _, wheel := range lock.PyPI {
		fileName, err := wheelPoolFileName(wheel.Filename)
		if err != nil {
			return nil, fmt.Errorf("%w: package %s: %w", ErrBadLock, wheel.Name, err)
		}
		if other, seen := seenFileNames[fileName]; seen {
			return nil, fmt.Errorf("%w: packages %s and %s share the file name %s", ErrBadLock, other, wheel.Name, fileName)
		}
		seenFileNames[fileName] = wheel.Name
		if !strings.HasPrefix(wheel.URL, "https://") {
			return nil, fmt.Errorf("%w: package %s has url %q, which is not https", ErrBadLock, wheel.Name, wheel.URL)
		}
		entries = append(entries, Entry{
			Package:  wheel.Name,
			Version:  wheel.Version,
			FileName: fileName,
			URL:      wheel.URL,
			Size:     wheel.Size,
			SHA256:   strings.ToLower(wheel.SHA256),
		})
	}
	return entries, nil
}

// FallbackURL returns the Fetch fallback for lock: Launchpad's librarian for
// a package of the Ubuntu archive, Launchpad's PPA files for a package of a
// PPA, nothing for other sources.
func FallbackURL(lock recipe.Lockfile) func(Entry) string {
	return func(entry Entry) string {
		if entry.Source == "" {
			if lock.Distro == "ubuntu" {
				return LaunchpadURL(entry)
			}
			return ""
		}
		if owner, name, isPPA := sources.PPAOf(entry.BaseURL); isPPA {
			return sources.PPAFilesURL(owner, name, entry.FileName)
		}
		return ""
	}
}

// poolFileName returns the base name of an index Filename, refusing anything
// that is not a plain relative path to a .deb file. The lock is written by
// frostroot, but it is also a text file anyone can edit.
func poolFileName(indexFileName string) (string, error) {
	cleaned := path.Clean(indexFileName)
	if indexFileName == "" || strings.HasPrefix(indexFileName, "/") || cleaned != indexFileName || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return "", fmt.Errorf("filename %q is not a plain relative path", indexFileName)
	}
	fileName := path.Base(cleaned)
	if !strings.HasSuffix(fileName, ".deb") || fileName == ".deb" || strings.ContainsAny(fileName, "\\\x00") {
		return "", fmt.Errorf("filename %q does not name a .deb file", indexFileName)
	}
	return fileName, nil
}

// wheelPoolFileName returns the base name of a lock's wheel file name,
// refusing anything that is not a plain .whl file name. The lock is written
// by frostroot, but it is also a text file anyone can edit.
func wheelPoolFileName(lockFileName string) (string, error) {
	cleaned := path.Clean(lockFileName)
	if lockFileName == "" || cleaned != lockFileName || strings.ContainsAny(lockFileName, `/\`+"\x00") {
		return "", fmt.Errorf("filename %q is not a plain file name", lockFileName)
	}
	if !strings.HasSuffix(cleaned, ".whl") || cleaned == ".whl" {
		return "", fmt.Errorf("filename %q does not name a .whl file", lockFileName)
	}
	return cleaned, nil
}

// TotalSize returns the size of every entry added up.
func TotalSize(entries []Entry) int64 {
	var total int64
	for _, entry := range entries {
		total += entry.Size
	}
	return total
}
