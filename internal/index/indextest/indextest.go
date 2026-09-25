// Package indextest serves a small Ubuntu archive for tests of what reads a
// package index: InRelease files with true sizes and digests, and the
// Packages files they list.
package indextest

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
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
