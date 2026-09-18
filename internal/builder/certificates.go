package builder

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"frostroot/internal/pki"
	"frostroot/internal/recipe"
)

// ImageCertificateDir is where update-ca-certificates reads the certificates
// added to an image from. A file there has to end in .crt whatever the
// recipe called it.
const ImageCertificateDir = "/usr/local/share/ca-certificates"

// ErrCertificate means a certificate the recipe names could not be read or
// is not a certificate. A build must not start without the authorities it
// will trust, for the same reason it must not start without a source's key.
var ErrCertificate = errors.New("certificate")

// StagedCertificate is one certificate as the image will hold it: one file
// per certificate, because update-ca-certificates hashes the first one in a
// file and ignores the rest.
type StagedCertificate struct {
	FileName string // <name>.crt, inside ImageCertificateDir
	PEM      []byte
}

// CertificateSet is what a build read from the recipe's [certificates]: the
// files to place in the image, what the lock records about them, and every
// certificate concatenated, which is what the build itself fetches with.
type CertificateSet struct {
	Files  []StagedCertificate
	Locked []recipe.LockCertificate
	PEM    []byte
}

// ReadCertificates reads every certificate file the recipe names, in order,
// and splits each into one staged file per certificate. A file holding a
// single certificate keeps its own name; the second and later certificates
// of a bundle get -2, -3 and so on, and two that would land on one file name
// are an error rather than a silent overwrite.
func ReadCertificates(recipeDir string, certificatePaths []string) (CertificateSet, error) {
	var set CertificateSet
	takenFileNames := map[string]string{}
	for _, certificatePath := range certificatePaths {
		if err := recipe.CheckCertificatePath(certificatePath); err != nil {
			return CertificateSet{}, fmt.Errorf("%w: %w", ErrCertificate, err)
		}
		data, err := os.ReadFile(recipe.CertificatePath(recipeDir, certificatePath))
		if err != nil {
			return CertificateSet{}, fmt.Errorf("%w: %s: %w", ErrCertificate, certificatePath, err)
		}
		certificates, err := pki.ParseCertificates(data)
		if err != nil {
			return CertificateSet{}, fmt.Errorf("%w: %s: %w", ErrCertificate, certificatePath, err)
		}
		name := recipe.CertificateName(certificatePath)
		for index, certificate := range certificates {
			fileName := name + ".crt"
			if index > 0 {
				fileName = name + "-" + strconv.Itoa(index+1) + ".crt"
			}
			if owner, taken := takenFileNames[fileName]; taken {
				return CertificateSet{}, fmt.Errorf("%w: %s and %s would both install as %s", ErrCertificate, owner, certificatePath, fileName)
			}
			takenFileNames[fileName] = certificatePath
			set.Files = append(set.Files, StagedCertificate{FileName: fileName, PEM: certificate.PEM})
			set.PEM = append(set.PEM, certificate.PEM...)
		}
		// The digest is of the recipe's file as written, not of what it
		// parsed to: the lock answers "is this still the file the build
		// read", and reformatting it is a change worth reporting.
		digest := sha256.Sum256(data)
		set.Locked = append(set.Locked, recipe.LockCertificate{
			Name:   name,
			Path:   certificatePath,
			SHA256: hex.EncodeToString(digest[:]),
		})
	}
	return set, nil
}

// HostTrustPath is the build host's own certificate store, which apt would
// use if it were not given another.
const HostTrustPath = "/etc/ssl/certs/ca-certificates.crt"

// writeAptCaInfo writes the bundle apt verifies HTTPS sources against into
// workDir and returns its path, or "" when the build trusts nothing beyond
// the host's own store, which is what apt uses by then anyway. The host's
// certificates come first, because Acquire::https::CaInfo replaces the store
// rather than adding to it: a network that inspects only some hosts must
// still verify the rest.
func writeAptCaInfo(workDir string, extraPEM []byte) (string, error) {
	if len(extraPEM) == 0 {
		return "", nil
	}
	hostPEM, err := os.ReadFile(HostTrustPath)
	if err != nil {
		// A host with no store of its own is not an error: the build then
		// trusts exactly what it was given.
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("reading %s: %w", HostTrustPath, err)
		}
		hostPEM = nil
	}
	caInfoPath := filepath.Join(workDir, "apt-ca-bundle.pem")
	bundle := append(append([]byte(nil), hostPEM...), extraPEM...)
	if err := os.WriteFile(caInfoPath, bundle, 0o644); err != nil {
		return "", err
	}
	return caInfoPath, nil
}

// CheckCertificatesAgainstLock reports how the recipe's certificate files
// differ from what the lock recorded, one message per difference. An offline
// rebuild installs the bytes beside the recipe, so a file that changed since
// the lock was written would quietly produce a different image.
func CheckCertificatesAgainstLock(read []recipe.LockCertificate, locked []recipe.LockCertificate) []string {
	var problems []string
	lockedDigests := map[string]string{}
	for _, certificate := range locked {
		lockedDigests[certificate.Path] = certificate.SHA256
	}
	for _, certificate := range read {
		digest, inLock := lockedDigests[certificate.Path]
		if !inLock {
			problems = append(problems, fmt.Sprintf("certificate %s is not in the lock", certificate.Path))
			continue
		}
		if digest != certificate.SHA256 {
			problems = append(problems, fmt.Sprintf("certificate %s changed since the lock was written (lock %s, file %s)", certificate.Path, shortDigest(digest), shortDigest(certificate.SHA256)))
		}
		delete(lockedDigests, certificate.Path)
	}
	for _, certificate := range locked {
		if _, missing := lockedDigests[certificate.Path]; missing {
			problems = append(problems, fmt.Sprintf("the lock has certificate %s, which the recipe no longer names", certificate.Path))
		}
	}
	return problems
}

// shortDigestLength is how much of a digest a message shows.
const shortDigestLength = 12

// shortDigest shortens a hex digest for a message.
func shortDigest(digest string) string {
	if len(digest) <= shortDigestLength {
		return digest
	}
	return digest[:shortDigestLength]
}
