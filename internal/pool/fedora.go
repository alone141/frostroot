package pool

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
)

// RPMsDirName is where a Fedora lock's vendored packages live, relative to
// the recipe directory. A package's file name is unique across a release's
// repositories, so they share one directory; the lock says which each came
// from.
const RPMsDirName = "vendor/rpms"

// DirName returns where lock's packages are vendored, relative to the recipe
// directory: vendor/rpms for a Fedora lock, vendor/debs otherwise.
func DirName(lock recipe.Lockfile) string {
	if family, err := distro.FamilyOf(lock.Distro); err == nil && family == distro.Fedora {
		return RPMsDirName
	}
	return DebsDirName
}

// kojiPackagesURL is where Fedora's build system keeps every build it made,
// an update long gone from the repositories included.
const kojiPackagesURL = "https://kojipkgs.fedoraproject.org/packages"

// Patterns for what a Fedora lock names, which reach a URL: the lock is
// written by frostroot, but it is also a text file anyone can edit.
var (
	rpmFieldPattern = regexp.MustCompile(`^[A-Za-z0-9._+~^-]+$`)
	rpmArchPattern  = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// fedoraManifest lists what a complete vendor/rpms holds for a Fedora lock:
// every package of the image, then every one of the tools tree it was made
// with, once each, in the lock's order.
func fedoraManifest(lock recipe.Lockfile) ([]Entry, error) {
	for _, repository := range lock.Repositories {
		parsed, err := url.Parse(repository.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return nil, fmt.Errorf("%w: repository %s has url %q, which is not a plain https URL", ErrBadLock, repository.Name, repository.URL)
		}
	}
	var entries []Entry
	seen := map[string]recipe.LockPackage{}
	for _, locked := range append(append([]recipe.LockPackage{}, lock.Packages...), lock.Tools...) {
		if locked.SHA256 == "" || locked.Size <= 0 || locked.Filename == "" {
			return nil, ErrNoChecksums
		}
		fileName, err := rpmPoolFileName(locked.Filename)
		if err != nil {
			return nil, fmt.Errorf("%w: package %s: %w", ErrBadLock, locked.Name, err)
		}
		if other, dup := seen[fileName]; dup {
			// The image and its tools tree share many packages, glibc
			// among them: one file, vendored once.
			if other.SHA256 != locked.SHA256 {
				return nil, fmt.Errorf("%w: packages %s and %s share the file name %s with different checksums", ErrBadLock, other.Name, locked.Name, fileName)
			}
			continue
		}
		seen[fileName] = locked
		repository, found := lock.Repository(locked.Source)
		if !found {
			return nil, fmt.Errorf("%w: package %s comes from repository %q, which the lock does not describe", ErrBadLock, locked.Name, locked.Source)
		}
		entries = append(entries, Entry{
			Package:   locked.Name,
			Version:   locked.Version,
			Arch:      locked.Arch,
			FileName:  fileName,
			URLPath:   locked.Filename,
			BaseURL:   repository.URL,
			Source:    locked.Source,
			SourceRPM: locked.SourceRPM,
			Size:      locked.Size,
			SHA256:    strings.ToLower(locked.SHA256),
		})
	}
	if len(entries) == 0 {
		return nil, ErrNoChecksums
	}
	return entries, nil
}

// rpmPoolFileName returns the base name of a Fedora lock's filename,
// refusing anything that is not a plain relative path to an .rpm file.
func rpmPoolFileName(lockFileName string) (string, error) {
	cleaned := path.Clean(lockFileName)
	if lockFileName == "" || strings.HasPrefix(lockFileName, "/") || cleaned != lockFileName || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return "", fmt.Errorf("filename %q is not a plain relative path", lockFileName)
	}
	fileName := path.Base(cleaned)
	if !strings.HasSuffix(fileName, ".rpm") || fileName == ".rpm" || strings.ContainsAny(fileName, "\\\x00") {
		return "", fmt.Errorf("filename %q does not name an .rpm file", lockFileName)
	}
	return fileName, nil
}

// kojiURL returns where Fedora's build system keeps entry's file, signed
// with the release's key as the repositories served it, or "" when the lock
// does not say enough to know: its source rpm names the build.
func kojiURL(entry Entry, keyID string) string {
	build, found := strings.CutSuffix(entry.SourceRPM, ".src.rpm")
	if !found || !rpmArchPattern.MatchString(entry.Arch) {
		return ""
	}
	nameVersion, release, found := cutLast(build, "-")
	if !found {
		return ""
	}
	name, version, found := cutLast(nameVersion, "-")
	if !found {
		return ""
	}
	for _, field := range []string{name, version, release} {
		if !rpmFieldPattern.MatchString(field) || field == "." || field == ".." {
			return ""
		}
	}
	return strings.Join([]string{kojiPackagesURL, name, version, release, "data", "signed", keyID, entry.Arch, url.PathEscape(entry.FileName)}, "/")
}

// fedoraKeyID returns how Fedora's build system names the release's key:
// the last eight digits of its fingerprint, lowercase.
func fedoraKeyID(lock recipe.Lockfile) string {
	release, err := distro.LookupFedora(lock.Release, distro.SupportedArch)
	if err != nil || len(release.KeyFingerprint) < 8 {
		return ""
	}
	return strings.ToLower(release.KeyFingerprint[len(release.KeyFingerprint)-8:])
}

// cutLast is strings.Cut around the last separator.
func cutLast(text, separator string) (before, after string, found bool) {
	index := strings.LastIndex(text, separator)
	if index < 0 {
		return text, "", false
	}
	return text[:index], text[index+len(separator):], true
}
