//go:build integration

package index

import (
	"context"
	"testing"
	"time"

	"frostroot/internal/distro"
	"frostroot/internal/form"
)

// TestIntegrationOpenEveryRelease fetches the real index of every supported
// release, about 20 MB each, and checks what the picker promises: every
// catalog entry is found by its exact name first, a slip of the fingers
// finds its way back, and a search is fast enough to run on every key.
func TestIntegrationOpenEveryRelease(t *testing.T) {
	for _, version := range distro.SupportedVersions() {
		release, err := distro.Lookup(version, distro.SupportedArch)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(version, func(t *testing.T) {
			started := time.Now()
			cacheDir := t.TempDir()
			opened, err := Open(context.Background(), Options{Release: release, CacheDir: cacheDir})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s, fetched and reduced in %v", opened.Describe(), time.Since(started).Round(time.Millisecond))
			if opened.Len() < 50_000 {
				t.Errorf("%d packages; a release has many more", opened.Len())
			}
			for _, entry := range form.Catalog() {
				matches, _ := opened.Search(entry.Name, "", 1)
				if len(matches) != 1 || matches[0].Name != entry.Name {
					t.Errorf("searching %q found %v first", entry.Name, matches)
				}
			}
			started = time.Now()
			nearest := opened.Nearest("ninja-buld", 3)
			nearestTook := time.Since(started)
			if len(nearest) == 0 || nearest[0] != "ninja-build" {
				t.Errorf("Nearest(ninja-buld) = %v", nearest)
			}
			started = time.Now()
			_, total := opened.Search("lib", "", 200)
			searchTook := time.Since(started)
			t.Logf("Nearest took %v; Search(lib) found %d in %v", nearestTook.Round(time.Microsecond), total, searchTook.Round(time.Microsecond))
			if searchTook > 100*time.Millisecond {
				t.Errorf("a search took %v: too slow to run on every key", searchTook)
			}

			started = time.Now()
			cached, err := Open(context.Background(), Options{Release: release, CacheDir: cacheDir, Offline: true})
			if err != nil || cached.Len() != opened.Len() {
				t.Fatalf("reopening from the cache: %v, %v", cached, err)
			}
			t.Logf("reopened from the cache in %v", time.Since(started).Round(time.Millisecond))
		})
	}
}
