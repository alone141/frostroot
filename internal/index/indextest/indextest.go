// Package indextest serves a small Ubuntu archive for tests of what reads a
// package index: InRelease files with true sizes and digests, and the
// Packages files they list; and the same of a Fedora release, and of PyPI.
package indextest

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"frostroot/internal/distro"
)

// Package is one package the archive offers.
type Package struct {
	Name, Version, Section, Description string
}

// Archive is a running test archive.
type Archive struct {
	URL      string
	requests atomic.Int64
	// Overrun, when set before the first request, is appended to the
	// Packages file as a second gzip member, so that the body runs past the
	// size the InRelease declares: what a server that lies about its size,
	// or a mirror mid-publication, sends. Its text would parse as more
	// stanzas if anything read that far.
	Overrun string
}

// Requests returns how many requests the archive has answered.
func (a *Archive) Requests() int { return int(a.requests.Load()) }

// Serve runs an archive whose release pocket of suite offers packages in
// main, gzip-compressed, and which has no -updates pocket, like a frozen
// mirror. It stops with the test.
func Serve(t *testing.T, suite string, packages []Package) *Archive {
	t.Helper()
	sorted := append([]Package(nil), packages...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var text strings.Builder
	for _, item := range sorted {
		fmt.Fprintf(&text, "Package: %s\nVersion: %s\nArchitecture: amd64\nSection: %s\nDescription: %s\n\n", item.Name, item.Version, item.Section, item.Description)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(text.String())); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(compressed.Bytes())
	indexPath := "main/binary-amd64/Packages.gz"
	release := fmt.Sprintf("Origin: Ubuntu\nSuite: %s\nSHA256:\n %s %d %s\n", suite, hex.EncodeToString(digest[:]), compressed.Len(), indexPath)
	files := map[string][]byte{
		"/dists/" + suite + "/InRelease":    []byte(release),
		"/dists/" + suite + "/" + indexPath: compressed.Bytes(),
	}
	archive := &Archive{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		archive.requests.Add(1)
		content, isServed := files[request.URL.Path]
		if !isServed {
			http.NotFound(writer, request)
			return
		}
		if archive.Overrun != "" && request.URL.Path == "/dists/"+suite+"/"+indexPath {
			var overrun bytes.Buffer
			extra := gzip.NewWriter(&overrun)
			_, _ = extra.Write([]byte(archive.Overrun))
			_ = extra.Close()
			content = append(append([]byte(nil), content...), overrun.Bytes()...)
		}
		_, _ = writer.Write(content)
	}))
	t.Cleanup(server.Close)
	archive.URL = server.URL
	return archive
}

// ServePyPI runs a PEP 691 simple index offering projects, the way
// pypi.org/simple does, and answers /pypi/<name>/json with a summary for
// the ones summaries names.
func ServePyPI(t *testing.T, projects []string, summaries map[string]string) *Archive {
	t.Helper()
	var document strings.Builder
	document.WriteString(`{"meta":{"api-version":"1.0"},"projects":[`)
	for position, name := range projects {
		if position > 0 {
			document.WriteString(",")
		}
		fmt.Fprintf(&document, `{"name":%q}`, name)
	}
	document.WriteString("]}")
	archive := &Archive{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		archive.requests.Add(1)
		if name, isSummary := strings.CutPrefix(request.URL.Path, "/pypi/"); isSummary {
			summary, isThere := summaries[strings.TrimSuffix(name, "/json")]
			if !isThere {
				http.NotFound(response, request)
				return
			}
			_, _ = fmt.Fprintf(response, `{"info":{"summary":%q}}`, summary)
			return
		}
		_, _ = response.Write([]byte(document.String()))
	}))
	t.Cleanup(server.Close)
	archive.URL = server.URL
	return archive
}

// FedoraMirror is a running test server holding a Fedora release's two
// repositories.
type FedoraMirror struct {
	Archive
	release distro.FedoraRelease
}

// Release is the release the mirror serves, with its repositories on it:
// what index.OpenFedora is given in place of the table's.
func (m *FedoraMirror) Release() distro.FedoraRelease { return m.release }

// ServeFedora runs a server for a Fedora release whose own repository
// offers packages, built for x86_64, and whose updates repository offers
// none, each with a repomd.xml giving the true size and digest of its
// gzip-compressed primary metadata. A package's Version is rpm's
// version-release, such as 2.55.0-1.fc44; its Section is not used.
func ServeFedora(t *testing.T, version string, packages []Package) *FedoraMirror {
	t.Helper()
	files := map[string][]byte{}
	publish := func(repositoryPath string, offered []Package) {
		var text strings.Builder
		fmt.Fprintf(&text, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<metadata xmlns=\"http://linux.duke.edu/metadata/common\" xmlns:rpm=\"http://linux.duke.edu/metadata/rpm\" packages=\"%d\">\n", len(offered))
		for _, item := range offered {
			ver, rel, _ := strings.Cut(item.Version, "-")
			fmt.Fprintf(&text, "<package type=\"rpm\"><name>%s</name><arch>x86_64</arch><version epoch=\"0\" ver=\"%s\" rel=\"%s\"/><summary>%s</summary></package>\n", html.EscapeString(item.Name), html.EscapeString(ver), html.EscapeString(rel), html.EscapeString(item.Description))
		}
		text.WriteString("</metadata>\n")
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write([]byte(text.String())); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(compressed.Bytes())
		href := "repodata/" + hex.EncodeToString(digest[:]) + "-primary.xml.gz"
		files[repositoryPath+"/"+href] = compressed.Bytes()
		files[repositoryPath+"/repodata/repomd.xml"] = []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo"><data type="primary"><checksum type="sha256">%s</checksum><location href="%s"/><size>%d</size><open-size>%d</open-size></data></repomd>
`, hex.EncodeToString(digest[:]), href, compressed.Len(), text.Len()))
	}
	releasePath, updatesPath := "/releases/"+version+"/Everything/x86_64/os", "/updates/"+version+"/Everything/x86_64"
	publish(releasePath, packages)
	publish(updatesPath, nil)
	mirror := &FedoraMirror{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mirror.requests.Add(1)
		content, isServed := files[request.URL.Path]
		if !isServed {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(content)
	}))
	t.Cleanup(server.Close)
	mirror.URL = server.URL
	mirror.release = distro.FedoraRelease{Version: version, Repositories: []distro.FedoraRepository{
		{ID: "fedora", BaseURL: server.URL + releasePath},
		{ID: "updates", BaseURL: server.URL + updatesPath},
	}}
	return mirror
}
