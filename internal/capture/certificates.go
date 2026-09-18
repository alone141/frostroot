package capture

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"frostroot/internal/pki"
	"frostroot/internal/recipe"
)

// Where an organization's own certificate authorities live on an installed
// system, relative to the root. update-ca-certificates reads the first and
// regenerates the second from it and from the ca-certificates package, so
// the first is the one a recipe can carry.
const (
	localCertificateDir = "usr/local/share/ca-certificates"
	trustedCertificates = "etc/ssl/certs"
)

// capturedCertificate is one authority the recipe can carry.
type capturedCertificate struct {
	path string // recipe-relative, such as certs/corp-root.pem
	pem  []byte
	from string // where on the machine it was read
}

// certificateNameSeparators replaces every run of characters a certificate
// file name cannot hold.
var certificateNameSeparators = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// maxCertificateNameLength mirrors the recipe's rule.
const maxCertificateNameLength = 64

// certificatesForRecipe reads the authorities an organization added to the
// machine. They live outside /etc, which is why the rest of capture does not
// see them, and they are exactly what a machine behind a TLS-inspecting
// proxy needs in its image. A file that does not parse is left for the
// report rather than written as a certificate.
func (root systemRoot) certificatesForRecipe() (carried []capturedCertificate, left []leftSource) {
	entries, err := os.ReadDir(root.path(localCertificateDir))
	if err != nil {
		return nil, nil
	}
	usedNames := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".crt") {
			continue
		}
		from := "/" + localCertificateDir + "/" + entry.Name()
		data, err := os.ReadFile(root.path(localCertificateDir + "/" + entry.Name()))
		if err != nil {
			left = append(left, leftSource{from, "could not be read: " + err.Error()})
			continue
		}
		certificates, err := pki.ParseCertificates(data)
		if err != nil {
			left = append(left, leftSource{from, err.Error()})
			continue
		}
		name := certificateNameFor(entry.Name(), usedNames)
		usedNames[name] = true
		carried = append(carried, capturedCertificate{
			path: recipe.CertificatesDirName + "/" + name + ".pem",
			pem:  pki.Concatenate(certificates),
			from: from,
		})
	}
	return carried, left
}

// certificateNameFor turns a .crt file name into one the recipe accepts,
// made unique against the names already taken.
func certificateNameFor(fileName string, usedNames map[string]bool) string {
	name := strings.TrimSuffix(fileName, ".crt")
	name = strings.Trim(certificateNameSeparators.ReplaceAllString(name, "-"), "-._")
	if name == "" {
		name = "certificate"
	}
	if len(name) > maxCertificateNameLength-3 {
		name = strings.TrimRight(name[:maxCertificateNameLength-3], "-._")
	}
	unique := name
	for suffix := 2; usedNames[unique]; suffix++ {
		unique = name + "-" + strconv.Itoa(suffix)
	}
	return unique
}

// unaccountedTrustFinding lists certificates in /etc/ssl/certs that neither
// the ca-certificates package nor the files capture carried explain.
// update-ca-certificates regenerates that directory, so the rest of capture
// ignores it; a regular file there that no package owns was put there by
// hand and will not survive into an image.
func (root systemRoot) unaccountedTrustFinding(owned map[string]bool, haveOwnership bool, carried []capturedCertificate) Finding {
	finding := Finding{
		Area:   AreaUnaccountedTrust,
		Advice: "update-ca-certificates rebuilds /etc/ssl/certs from the ca-certificates package and /usr/local/share/ca-certificates, so a file only in /etc/ssl/certs is not carried. Move it to /usr/local/share/ca-certificates on the machine and capture again, or name it in [certificates] by hand.",
	}
	if !haveOwnership {
		finding.Unavailable = "dpkg's file lists could not be read, so which package owns a certificate is unknown."
		return finding
	}
	entries, err := os.ReadDir(root.path(trustedCertificates))
	if err != nil {
		finding.Unavailable = "/" + trustedCertificates + " could not be listed."
		return finding
	}
	carriedNames := map[string]bool{}
	for _, certificate := range carried {
		carriedNames[strings.TrimSuffix(filepath.Base(certificate.from), ".crt")] = true
	}
	var unaccounted []string
	for _, entry := range entries {
		// Only a regular file: update-ca-certificates writes symlinks for
		// everything it knows about, and the hash links beside them.
		if entry.IsDir() || !entry.Type().IsRegular() {
			continue
		}
		path := "/" + trustedCertificates + "/" + entry.Name()
		if owned[path] || entry.Name() == "ca-certificates.crt" {
			continue
		}
		// A local authority lands here as <name>.pem beside its .crt.
		if carriedNames[strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))] {
			continue
		}
		unaccounted = append(unaccounted, path)
	}
	slices.Sort(unaccounted)
	finding.Count, finding.Examples = len(unaccounted), unaccounted
	return finding
}

// certificatePaths returns the recipe-relative certificate paths, sorted, so
// that a capture of one machine always writes the same recipe.
func (s Snapshot) certificatePaths() []string {
	paths := make([]string, 0, len(s.Certificates))
	for path := range s.Certificates {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}
