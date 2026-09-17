package deb

import (
	// MD5 is here because apt's Packages and Release formats list MD5sum
	// beside SHA256; nothing is trusted on its strength.
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// FlatRelease describes the Release file of a flat repository.
type FlatRelease struct {
	Suite string    // written as Suite and Codename; apt patterns such as ?codename(^noble$) match on these
	Arch  string    // written as Architectures
	Date  time.Time // written in UTC
}

// Files a flat repository consists of, beside the packages.
const (
	PackagesFileName = "Packages"
	ReleaseFileName  = "Release"
)

// WriteFlatRepository turns dir, holding the .deb files named in
// debFileNames, into a flat apt repository: it writes Packages, one paragraph
// per file made of the package's own control file plus Filename, Size,
// MD5sum and SHA256, and Release, which names the suite and carries the
// hashes of Packages. Files are listed in name order, so the output depends
// only on the inputs. A "deb [trusted=yes] copy://<dir> ./" source line
// installs from the result.
func WriteFlatRepository(dir string, release FlatRelease, debFileNames []string) error {
	var packages strings.Builder
	for _, debFileName := range slices.Sorted(slices.Values(debFileNames)) {
		debPath := filepath.Join(dir, debFileName)
		control, err := ReadControl(debPath)
		if err != nil {
			return err
		}
		size, digests, err := hashFile(debPath, md5.New(), sha256.New())
		if err != nil {
			return err
		}
		fmt.Fprintf(&packages, "%s\nFilename: ./%s\nSize: %d\nMD5sum: %s\nSHA256: %s\n\n", control.Text, debFileName, size, digests[0], digests[1])
	}
	packagesBytes := []byte(packages.String())
	if err := os.WriteFile(filepath.Join(dir, PackagesFileName), packagesBytes, 0o644); err != nil {
		return err
	}
	md5Digest, sha256Digest := md5.Sum(packagesBytes), sha256.Sum256(packagesBytes)
	releaseText := fmt.Sprintf("Origin: frostroot\nLabel: frostroot vendored packages\nSuite: %s\nCodename: %s\nArchitectures: %s\nDate: %s\nDescription: packages from frostroot.lock\nMD5Sum:\n %s %d %s\nSHA256:\n %s %d %s\n",
		release.Suite, release.Suite, release.Arch, release.Date.UTC().Format("Mon, 02 Jan 2006 15:04:05 UTC"),
		hex.EncodeToString(md5Digest[:]), len(packagesBytes), PackagesFileName,
		hex.EncodeToString(sha256Digest[:]), len(packagesBytes), PackagesFileName)
	return os.WriteFile(filepath.Join(dir, ReleaseFileName), []byte(releaseText), 0o644)
}

// hashFile returns the size of the file at path and its digests by every
// hash given, as lowercase hex, in the same order.
func hashFile(path string, hashes ...hash.Hash) (size int64, digests []string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = file.Close() }() // read-only: closing cannot lose data
	writers := make([]io.Writer, len(hashes))
	for i, digest := range hashes {
		writers[i] = digest
	}
	size, err = io.Copy(io.MultiWriter(writers...), file)
	if err != nil {
		return 0, nil, fmt.Errorf("reading %s: %w", path, err)
	}
	for _, digest := range hashes {
		digests = append(digests, hex.EncodeToString(digest.Sum(nil)))
	}
	return size, digests, nil
}

// SHA256File returns the size and the SHA-256 digest, as lowercase hex, of
// the file at path.
func SHA256File(path string) (size int64, digest string, err error) {
	size, digests, err := hashFile(path, sha256.New())
	if err != nil {
		return 0, "", err
	}
	return size, digests[0], nil
}
