//go:build integration

package form

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"frostroot/internal/distro"
)

// TestIntegrationCatalogExistsInEveryRelease reads the archive's package
// indexes for every supported release and checks that each catalog entry
// is a real package there, so init never offers a name apt cannot install.
// It downloads about 50 MB and needs the network.
func TestIntegrationCatalogExistsInEveryRelease(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Minute}
	for _, version := range distro.SupportedVersions() {
		release, err := distro.Lookup(version, distro.SupportedArch)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(version, func(t *testing.T) {
			available := map[string]bool{}
			for _, component := range release.Components {
				indexURL := fmt.Sprintf("%s/dists/%s/%s/binary-%s/Packages.gz", release.ArchiveURL, release.Suite, component, distro.SupportedArch)
				for name := range packageNames(t, client, indexURL) {
					available[name] = true
				}
			}
			for _, entry := range Catalog() {
				if !available[entry.Name] {
					t.Errorf("%s is not in Ubuntu %s (%s)", entry.Name, version, release.Suite)
				}
			}
		})
	}
}

// packageNames returns the names listed in a compressed Packages index.
func packageNames(t *testing.T, client *http.Client, indexURL string) map[string]bool {
	t.Helper()
	response, err := client.Get(indexURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }() // read-only
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s", indexURL, response.Status)
	}
	decompressed, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	scanner := bufio.NewScanner(decompressed)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if name, isPackageLine := strings.CutPrefix(scanner.Text(), "Package: "); isPackageLine {
			names[name] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}
