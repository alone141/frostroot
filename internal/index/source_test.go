package index

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"frostroot/internal/index/indextest"
)

// dockerPackages is what a vendor's repository offers: one suite, one
// component, no -updates pocket.
var dockerPackages = []indextest.Package{
	{Name: "containerd.io", Version: "1.7.22-1", Section: "admin", Description: "An open and reliable container runtime"},
	{Name: "docker-ce", Version: "5:27.3.1-1~ubuntu.24.04~noble", Section: "admin", Description: "Docker: the open-source application container engine"},
}

func sourceOptionsFor(t *testing.T) Options {
	t.Helper()
	return Options{CacheDir: t.TempDir(), Now: func() time.Time { return testNow }}
}

func TestOpenSourceIndexesAThirdPartyRepository(t *testing.T) {
	served := indextest.Serve(t, "noble", dockerPackages)
	options := sourceOptionsFor(t)
	docker := Source{Name: "docker", URL: served.URL, Suite: "noble", Components: []string{"main"}}

	opened, err := OpenSource(context.Background(), options, docker)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(opened.entries); !slices.Equal(got, []string{"containerd.io", "docker-ce"}) {
		t.Errorf("entries = %v, want the repository's packages", got)
	}
	// Every entry says where it came from, so a row can name the repository
	// a person is deciding to install from.
	for _, entry := range opened.entries {
		if entry.Origin != "docker" {
			t.Errorf("%s came from %q, want the source's name", entry.Name, entry.Origin)
		}
	}
	if !strings.HasPrefix(opened.Describe(), "docker · 2 packages") {
		t.Errorf("Describe = %q, want the source's own name", opened.Describe())
	}
	// Two requests and no more: the InRelease and the one Packages file.
	// A third would be the -updates pocket, which is Ubuntu's scheme and
	// not a vendor's, and would cost every open a 404.
	if served.Requests() != 2 {
		t.Errorf("requests = %d, want the InRelease and the Packages file alone", served.Requests())
	}

	// The second open is answered from the cache.
	reopened, err := OpenSource(context.Background(), options, docker)
	if err != nil {
		t.Fatal(err)
	}
	if served.Requests() != 2 || reopened.Len() != 2 {
		t.Errorf("requests = %d after a second open of %d packages, want the cache to answer", served.Requests(), reopened.Len())
	}
}

func TestSourceCacheDoesNotCollideWithTheArchives(t *testing.T) {
	// A PPA publishes under the release's own code name, so a cache file
	// named after the suite alone would have the two overwrite each other.
	archive := newArchive(t, ".gz")
	ppa := indextest.Serve(t, "noble", []indextest.Package{{Name: "python3.13", Version: "3.13.0-1", Section: "python", Description: "Interactive high-level object-oriented language"}})
	options := optionsFor(t, archive)

	release, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	source, err := OpenSource(context.Background(), options, Source{Name: "deadsnakes", URL: ppa.URL, Suite: "noble", Components: []string{"main"}})
	if err != nil {
		t.Fatal(err)
	}
	if source.Has("git") || !source.Has("python3.13") {
		t.Errorf("the source index holds %v, want the PPA's packages alone", names(source.entries))
	}
	if !release.Has("git") || release.Has("python3.13") {
		t.Errorf("the archive index gained the PPA's packages: %v", release.Has("python3.13"))
	}
	// Reopening the archive must still find its own cache, not the PPA's.
	again, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Has("git") || again.Has("python3.13") {
		t.Error("the archive's cache was overwritten by the source's")
	}
	entries, err := os.ReadDir(options.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("cache holds %d files, want one for the archive and one for the source", len(entries))
	}
}

func TestOpenSourceIgnoresTheArchiveMirror(t *testing.T) {
	// --mirror names an Ubuntu mirror. Applying it to a source would fetch
	// the mirror and call the result the source's packages.
	served := indextest.Serve(t, "stable", dockerPackages)
	options := sourceOptionsFor(t)
	options.Mirror = "http://mirror.example.invalid/ubuntu"
	opened, err := OpenSource(context.Background(), options, Source{Name: "docker", URL: served.URL, Suite: "stable", Components: []string{"main"}})
	if err != nil {
		t.Fatal(err)
	}
	if !opened.Has("docker-ce") {
		t.Errorf("the source was fetched from somewhere else: %v", names(opened.entries))
	}
}

func TestOpenSourceThatIsNotThere(t *testing.T) {
	served := indextest.Serve(t, "noble", dockerPackages)
	options := sourceOptionsFor(t)
	_, err := OpenSource(context.Background(), options, Source{Name: "docker", URL: served.URL, Suite: "bookworm", Components: []string{"main"}})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable so the caller can search the rest", err)
	}
	cached, readErr := os.ReadDir(options.CacheDir)
	if readErr == nil && len(cached) != 0 {
		t.Errorf("a repository that answered nothing was cached: %v", cached)
	}
}

func TestUnionSearchesEveryRepositoryItWasGiven(t *testing.T) {
	release := newIndex("noble", testNow, []Entry{
		{Name: "cmake", Version: "3.28.3-1build7", Component: "main", Section: "devel", Description: "cross-platform make"},
		{Name: "git", Version: "1:2.43.0-1", Component: "main", Section: "vcs", Description: "revision control"},
	}, func() time.Time { return testNow })
	kitware := newIndex("kitware", testNow.Add(-48*time.Hour), []Entry{
		{Name: "cmake", Version: "4.0.1-0kitware1", Component: "main", Section: "devel", Description: "cross-platform make", Origin: "kitware"},
	}, func() time.Time { return testNow })

	merged := Union([]*Index{release, kitware}, []string{"docker"})
	if merged.Len() != 2 {
		t.Errorf("merged holds %v, want one entry a name", names(merged.entries))
	}
	// A name both offer is shown as the source's: that repository was added
	// deliberately, and it is usually there for a newer version.
	cmake, isThere := merged.Lookup("cmake")
	if !isThere || cmake.Version != "4.0.1-0kitware1" || cmake.Origin != "kitware" {
		t.Errorf("cmake = %+v, want the source's", cmake)
	}
	if !merged.Has("git") {
		t.Error("the archive's own packages must survive the merge")
	}
	// The line above the results says what was searched and what could not
	// be reached, so a name missing because Docker was down does not read
	// as a name that does not exist.
	describe := merged.Describe()
	if !strings.Contains(describe, "noble + kitware") || !strings.Contains(describe, "docker not reachable") {
		t.Errorf("Describe = %q", describe)
	}
	// The oldest part decides the age: the freshest piece must not make a
	// week-old one look new.
	if !strings.Contains(describe, "2 days ago") {
		t.Errorf("Describe = %q, want the age of the oldest part", describe)
	}
}

func TestUnionOfOneIsThatOne(t *testing.T) {
	only := newIndex("noble", testNow, []Entry{{Name: "git"}}, func() time.Time { return testNow })
	if merged := Union([]*Index{only, nil}, nil); merged != only {
		t.Errorf("Union of one index = %v, want the index itself", merged)
	}
	if merged := Union(nil, nil); merged != nil {
		t.Errorf("Union of nothing = %v, want nil", merged)
	}
}

func TestUnionNamesTheRepositoryItCouldNotRead(t *testing.T) {
	release := newIndex("noble", testNow, []Entry{{Name: "git", Section: "vcs"}}, func() time.Time { return testNow })
	// A source served from a cache because the repository would not answer
	// is missing in the way that matters: what it holds may be out of date,
	// and what it has gained is not there at all.
	docker := newIndex("docker", testNow.Add(-9*24*time.Hour), []Entry{{Name: "docker-ce", Origin: "docker"}}, func() time.Time { return testNow })
	docker.missing = []string{"docker"}

	merged := Union([]*Index{release, docker}, nil)
	if got := merged.Describe(); !strings.Contains(got, "docker not reachable") || strings.Contains(got, "archive not reachable") {
		t.Errorf("Describe = %q, want the source named and the archive left alone", got)
	}
	searched, missing := merged.Sources()
	if !slices.Equal(searched, []string{"noble", "docker"}) || !slices.Equal(missing, []string{"docker"}) {
		t.Errorf("Sources = %v, %v; want both searched and the source missing", searched, missing)
	}
}

func TestSectionsLeaveOutTheOneAStanzaDidNotName(t *testing.T) {
	// A vendor's Packages stanza often has no Section. An empty one is not
	// a section to narrow to: the chooser offers "all sections" already, and
	// a second row of that name would filter by nothing at all.
	vendor := newIndex("docker", testNow, []Entry{
		{Name: "containerd.io", Origin: "docker"},
		{Name: "docker-ce", Section: "admin", Origin: "docker"},
	}, func() time.Time { return testNow })
	for _, section := range vendor.SectionsMatching("") {
		if section.Name == "" {
			t.Errorf("sections = %+v, want no row for a package with no section", vendor.SectionsMatching(""))
		}
	}
	// The package is still there; it is only the section that is not.
	if !vendor.Has("containerd.io") {
		t.Error("a package with no section must still be findable")
	}
}

func TestSourceCacheIsTriedAgainSoonerThanTheArchives(t *testing.T) {
	served := indextest.Serve(t, "noble", dockerPackages)
	now := testNow
	options := Options{CacheDir: t.TempDir(), Now: func() time.Time { return now }}
	docker := Source{Name: "docker", URL: served.URL, Suite: "noble", Components: []string{"main"}}
	if _, err := OpenSource(context.Background(), options, docker); err != nil {
		t.Fatal(err)
	}
	fetches := served.Requests()

	// Later the same day the cache answers: a form opened twice in an
	// afternoon stays off the network.
	now = testNow.Add(6 * time.Hour)
	if _, err := OpenSource(context.Background(), options, docker); err != nil {
		t.Fatal(err)
	}
	if served.Requests() != fetches {
		t.Errorf("%d requests after six hours, want the cache to answer", served.Requests()-fetches)
	}

	// Two days on it is fetched again, where the archive would be kept for
	// a week: a vendor publishes when it likes, and its index is kilobytes.
	now = testNow.Add(2 * 24 * time.Hour)
	if _, err := OpenSource(context.Background(), options, docker); err != nil {
		t.Fatal(err)
	}
	if served.Requests() == fetches {
		t.Error("a two-day-old source index was not fetched again")
	}
}
