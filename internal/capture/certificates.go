package capture

import (
	"io/fs"
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
// proxy needs in its image. update-ca-certificates trusts every .crt below
// the directory and follows symlinks, so the whole tree is read, and a
// certificate in a subdirectory is named after its path. Whatever cannot be
// carried is left for the report rather than dropped: a file that does not
// parse, a symlink that leaves the root, which with --root DIR would be
// this machine's file and not the captured one's, and a symlink to a
// directory, which is not descended. An authority the machine trusts is
// carried or named, never neither.
func (root systemRoot) certificatesForRecipe() (carried []capturedCertificate, left []leftSource) {
	base := root.path(localCertificateDir)
	if _, err := os.Lstat(base); err != nil {
		return nil, nil
	}
	usedNames := map[string]bool{}
	_ = filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		from := root.relativePath(path)
		if err != nil {
			left = append(left, leftSource{from, "could not be listed: " + err.Error()})
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		relative := strings.TrimPrefix(from, "/")
		filePath := root.path(relative)
		if entry.Type()&fs.ModeSymlink != 0 {
			resolved, inside := root.pathInRoot(relative)
			if !inside {
				left = append(left, leftSource{from, "a symlink out of " + string(root) + ", which belongs to this machine rather than the one being captured"})
				return nil
			}
			if info, err := os.Stat(resolved); err == nil && info.IsDir() {
				left = append(left, leftSource{from, "a symlink to a directory, which is not followed; name its certificates in [certificates] by hand"})
				return nil
			}
			filePath = resolved
		}
		if !strings.HasSuffix(entry.Name(), ".crt") {
			return nil
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			left = append(left, leftSource{from, "could not be read: " + err.Error()})
			return nil
		}
		certificates, err := pki.ParseCertificates(data)
		if err != nil {
			left = append(left, leftSource{from, err.Error()})
			return nil
		}
		name := certificateNameFor(strings.TrimPrefix(from, "/"+localCertificateDir+"/"), usedNames)
		usedNames[name] = true
		carried = append(carried, capturedCertificate{
			path: recipe.CertificatesDirName + "/" + name + ".pem",
			pem:  pki.Concatenate(certificates),
			from: from,
		})
		return nil
	})
	return carried, left
}

// certificateNameFor turns a .crt file name, or its path below the local
// certificate directory, into one the recipe accepts, made unique against
// the names already taken.
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
