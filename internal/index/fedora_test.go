package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"frostroot/internal/distro"
)

// fedoraMirror is a fake of Fedora's server holding a release's two
// repositories. It serves the excerpts in testdata, which are real packages
// of Fedora 44's primary metadata with their dependency and file lists cut
// to three entries each.
type fedoraMirror struct {
	t      *testing.T
	server *httptest.Server
	mutex  sync.Mutex
	files  map[string][]byte // by URL path
	served map[string]int    // requests by URL path
	// intercept, when set, may answer a request itself.
	intercept func(writer http.ResponseWriter, request *http.Request) bool
}

// The repositories' paths on the fake server, as on Fedora's.
const (
	fedoraReleasePath = "/pub/fedora/linux/releases/44/Everything/x86_64/os"
	fedoraUpdatesPath = "/pub/fedora/linux/updates/44/Everything/x86_64"
)

// newFedoraMirror serves Fedora 44 with its primary metadata compressed as
// extension says: ".zst", ".xz", ".gz" or "".
func newFedoraMirror(t *testing.T, extension string) *fedoraMirror {
	t.Helper()
	mirror := &fedoraMirror{t: t, files: map[string][]byte{}, served: map[string]int{}}
	mirror.publish(fedoraReleasePath, readTestdata(t, "fedora-44.primary.xml"), extension, 0)
	mirror.publish(fedoraUpdatesPath, readTestdata(t, "fedora-44-updates.primary.xml"), extension, 0)
	mirror.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mirror.mutex.Lock()
		mirror.served[request.URL.Path]++
		content, isServed := mirror.files[request.URL.Path]
		intercept := mirror.intercept
		mirror.mutex.Unlock()
		if intercept != nil && intercept(writer, request) {
			return
		}
		if !isServed {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(content)
	}))
	t.Cleanup(mirror.server.Close)
	return mirror
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// publish puts a repository's primary metadata in place under a new name,
// as createrepo_c names it after its digest, and the repomd.xml that lists
// it. openSize is what repomd.xml declares of the text; 0 declares its
// true size.
func (m *fedoraMirror) publish(repositoryPath string, text []byte, extension string, openSize int64) (primaryPath string) {
	m.t.Helper()
	content := compress(m.t, extension, text)
	digest := sha256.Sum256(content)
	openDigest := sha256.Sum256(text)
	if openSize == 0 {
		openSize = int64(len(text))
	}
	href := "repodata/" + hex.EncodeToString(digest[:]) + "-primary.xml" + extension
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.files[repositoryPath+"/"+href] = content
	m.files[repositoryPath+"/repodata/repomd.xml"] = []byte(repomdListing(href, hex.EncodeToString(digest[:]), hex.EncodeToString(openDigest[:]), int64(len(content)), openSize))
	return repositoryPath + "/" + href
}

// repomdListing is a repomd.xml as createrepo_c writes one, with the
// filelists entry before the primary one, as in Fedora's.
func repomdListing(href, digest, openDigest string, size, openSize int64) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo" xmlns:rpm="http://linux.duke.edu/metadata/rpm">
  <revision>1776864872</revision>
  <data type="filelists">
    <checksum type="sha256">326f2b47a15d5750b2bd3387831a9335731033b031cf2d5a9ffd26106d48648b</checksum>
    <location href="repodata/326f2b47a15d5750b2bd3387831a9335731033b031cf2d5a9ffd26106d48648b-filelists.xml.zst"/>
    <size>46935343</size>
    <open-size>829071915</open-size>
  </data>
  <data type="primary">
    <checksum type="sha256">%s</checksum>
    <open-checksum type="sha256">%s</open-checksum>
    <location href="%s"/>
    <timestamp>1776864859</timestamp>
    <size>%d</size>
    <open-size>%d</open-size>
  </data>
</repomd>
`, digest, openDigest, href, size, openSize)
}

// release is Fedora 44 as the table has it, on the fake server.
func (m *fedoraMirror) release() distro.FedoraRelease {
	return distro.FedoraRelease{Version: "44", Repositories: []distro.FedoraRepository{
		{ID: "fedora", BaseURL: m.server.URL + fedoraReleasePath},
		{ID: "updates", BaseURL: m.server.URL + fedoraUpdatesPath + "/"},
	}}
}

// requests returns how many requests the mirror has answered.
func (m *fedoraMirror) requests() int {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	total := 0
	for _, count := range m.served {
		total += count
	}
	return total
}

// requestsFor returns how many requests asked for path.
func (m *fedoraMirror) requestsFor(path string) int {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.served[path]
}

// fedoraOptions caches in a directory of the test's.
func fedoraOptions(t *testing.T) Options {
	t.Helper()
	return Options{CacheDir: t.TempDir(), Now: func() time.Time { return testNow }}
}

// fedora44Versions is what the excerpts come to: each name once, at the
// version dnf would install.
var fedora44Versions = map[string]string{
	"git":              "2.55.0-1.fc44",     // the updates repository's, in place of the release's 2.53.0
	"glibc":            "2.43-8.fc44",       // x86_64's: the i686 build beside it is another architecture
	"nano":             "8.7.1-1.fc44",      // only the release lists it
	"ninja-build":      "1.13.2-2.fc44",     //
	"python3-requests": "2.32.5-5.fc44",     // noarch
	"rubygem-bundler":  "4.0.3-32.fc44",     // the newer of the two the release lists, whichever comes first
	"vim-enhanced":     "2:9.2.1129-1.fc44", // with its epoch
}

func TestOpenFedoraFetchesReducesAndCaches(t *testing.T) {
	for _, extension := range []string{".zst", ".xz", ".gz", ""} {
		t.Run("primary.xml"+extension, func(t *testing.T) {
			mirror := newFedoraMirror(t, extension)
			options := fedoraOptions(t)
			var lastDone, lastTotal int64
			options.Progress = func(done, total int64) { lastDone, lastTotal = done, total }

			opened, err := OpenFedora(context.Background(), options, mirror.release())
			if err != nil {
				t.Fatal(err)
			}
			if opened.Len() != len(fedora44Versions) {
				t.Errorf("Len = %d, want %d: %v", opened.Len(), len(fedora44Versions), names(opened.entries))
			}
			for name, version := range fedora44Versions {
				if entry, found := opened.Lookup(name); !found || entry.Version != version {
					t.Errorf("%s = %+v, %v; want version %s", name, entry, found, version)
				}
			}
			if git, _ := opened.Lookup("git"); git.Description != "Fast Version Control System" || git.Origin != "" || git.Section != "" {
				t.Errorf("git = %+v, want its summary, and no source or section", git)
			}
			if sections := opened.SectionsMatching(""); len(sections) != 0 {
				t.Errorf("sections = %v; Fedora has none worth narrowing to", sections)
			}
			if got := opened.Describe(); got != "Fedora 44 · 7 packages · fetched just now" {
				t.Errorf("Describe = %q", got)
			}
			if lastTotal == 0 || lastDone != lastTotal {
				t.Errorf("progress ended at %d of %d, want all of a known total", lastDone, lastTotal)
			}

			// The second open reads the cache and asks the server nothing.
			before := mirror.requests()
			again, err := OpenFedora(context.Background(), options, mirror.release())
			if err != nil {
				t.Fatal(err)
			}
			if mirror.requests() != before {
				t.Errorf("a fresh cache still cost %d requests", mirror.requests()-before)
			}
			if !slices.Equal(again.entries, opened.entries) {
				t.Errorf("the cached index differs from the fetched one:\n%v\n%v", again.entries, opened.entries)
			}
			if _, err := os.Stat(filepath.Join(options.CacheDir, "fedora-44-amd64.tsv.gz")); err != nil {
				t.Errorf("the cache is not where it is named after the release: %v", err)
			}
		})
	}
}

// TestOpenFedoraKeepsItsRules: a week, --refresh-index, nothing fetched
// offline, and a stale cache when the server is gone, as for Ubuntu.
func TestOpenFedoraKeepsItsRules(t *testing.T) {
	mirror := newFedoraMirror(t, ".zst")
	options := fedoraOptions(t)
	options.Offline = true
	if _, err := OpenFedora(context.Background(), options, mirror.release()); !errors.Is(err, ErrUnavailable) || mirror.requests() != 0 {
		t.Fatalf("offline without a cache: err %v after %d requests, want ErrUnavailable and none", err, mirror.requests())
	}
	options.Offline = false
	if _, err := OpenFedora(context.Background(), options, mirror.release()); err != nil {
		t.Fatal(err)
	}
	before := mirror.requests()
	options.Now = func() time.Time { return testNow.Add(6 * 24 * time.Hour) }
	if _, err := OpenFedora(context.Background(), options, mirror.release()); err != nil || mirror.requests() != before {
		t.Fatalf("a six-day-old cache: err %v, %d new requests", err, mirror.requests()-before)
	}
	options.Refresh = true
	if _, err := OpenFedora(context.Background(), options, mirror.release()); err != nil || mirror.requests() == before {
		t.Fatalf("--refresh-index: err %v, %d new requests, want a fetch", err, mirror.requests()-before)
	}
	options.Refresh = false
	before = mirror.requests()
	// The refresh was on the sixth day; eight days after it the cache is old.
	options.Now = func() time.Time { return testNow.Add(14 * 24 * time.Hour) }
	if _, err := OpenFedora(context.Background(), options, mirror.release()); err != nil || mirror.requests() == before {
		t.Fatalf("an eight-day-old cache: err %v, %d new requests, want a fetch", err, mirror.requests()-before)
	}

	mirror.server.Close()
	options.Now = func() time.Time { return testNow.Add(30 * 24 * time.Hour) }
	stale, err := OpenFedora(context.Background(), options, mirror.release())
	if err != nil {
		t.Fatalf("a stale cache and no server: %v", err)
	}
	if got := stale.Describe(); got != "Fedora 44 · 7 packages · fetched 16 days ago · repositories not reachable" {
		t.Errorf("Describe = %q", got)
	}
	if _, missing := stale.Sources(); !slices.Equal(missing, []string{"repositories"}) {
		t.Errorf("missing = %q", missing)
	}
}

// TestOpenFedoraTriesARepublishedRepositoryOnceMore: Fedora's updates are
// republished every day, under new file names. A primary file cut short, or
// gone, is asked for again from repomd.xml on; one that stays wrong is given
// up on, and nothing is cached.
func TestOpenFedoraTriesARepublishedRepositoryOnceMore(t *testing.T) {
	t.Run("cut short", func(t *testing.T) {
		mirror := newFedoraMirror(t, ".zst")
		primary := mirror.publish(fedoraUpdatesPath, readTestdata(t, "fedora-44-updates.primary.xml"), ".zst", 0)
		good := mirror.files[primary]
		failures := 1
		mirror.intercept = func(writer http.ResponseWriter, request *http.Request) bool {
			mirror.mutex.Lock()
			defer mirror.mutex.Unlock()
			if request.URL.Path != primary || failures == 0 {
				return false
			}
			failures--
			_, _ = writer.Write(good[:len(good)/2])
			return true
		}
		opened, err := OpenFedora(context.Background(), fedoraOptions(t), mirror.release())
		if err != nil || opened.Len() != len(fedora44Versions) {
			t.Fatalf("OpenFedora = %v, %v; want the index after a second try", opened, err)
		}
		if got := mirror.requestsFor(fedoraUpdatesPath + "/repodata/repomd.xml"); got != 2 {
			t.Errorf("repomd.xml was read %d times, want twice", got)
		}
	})
	t.Run("gone", func(t *testing.T) {
		mirror := newFedoraMirror(t, ".zst")
		text := readTestdata(t, "fedora-44-updates.primary.xml")
		// The first repomd.xml names a file that is gone by the time it is
		// asked for; reading repomd.xml again finds today's.
		stale := mirror.publish(fedoraUpdatesPath, bytes.Replace(text, []byte("2.55.0"), []byte("2.54.0"), 1), ".zst", 0)
		staleListing := mirror.files[fedoraUpdatesPath+"/repodata/repomd.xml"]
		delete(mirror.files, stale)
		mirror.publish(fedoraUpdatesPath, text, ".zst", 0)
		listings := 0
		mirror.intercept = func(writer http.ResponseWriter, request *http.Request) bool {
			if request.URL.Path != fedoraUpdatesPath+"/repodata/repomd.xml" {
				return false
			}
			mirror.mutex.Lock()
			listings++
			first := listings == 1
			mirror.mutex.Unlock()
			if !first {
				return false
			}
			_, _ = writer.Write(staleListing)
			return true
		}
		opened, err := OpenFedora(context.Background(), fedoraOptions(t), mirror.release())
		if err != nil {
			t.Fatal(err)
		}
		if git, _ := opened.Lookup("git"); git.Version != "2.55.0-1.fc44" {
			t.Errorf("git = %+v, want today's", git)
		}
	})
	t.Run("wrong for good", func(t *testing.T) {
		mirror := newFedoraMirror(t, ".zst")
		options := fedoraOptions(t)
		primary := mirror.publish(fedoraUpdatesPath, readTestdata(t, "fedora-44-updates.primary.xml"), ".zst", 0)
		mirror.intercept = func(writer http.ResponseWriter, request *http.Request) bool {
			if request.URL.Path != primary {
				return false
			}
			_, _ = writer.Write(compress(t, ".zst", []byte(`<metadata><package type="rpm"><name>evil</name><arch>noarch</arch><version ver="1" rel="1"/></package></metadata>`)))
			return true
		}
		if _, err := OpenFedora(context.Background(), options, mirror.release()); !errors.Is(err, ErrUnavailable) || !errors.Is(err, errChanged) {
			t.Errorf("err = %v, want ErrUnavailable wrapping the mismatch", err)
		}
		if _, err := os.Stat(cachePath(options, fedoraTarget(mirror.release()))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("an index that failed its check was cached: %v", err)
		}
	})
}

// TestOpenFedoraRefusesMetadataLongerThanDeclared: the size repomd.xml
// lists bounds the download, and its open-size bounds what that expands
// to, each at the byte past it. Neither is asked for again: a file that is
// too long stays too long.
func TestOpenFedoraRefusesMetadataLongerThanDeclared(t *testing.T) {
	t.Run("the download", func(t *testing.T) {
		mirror := newFedoraMirror(t, ".zst")
		options := fedoraOptions(t)
		primary := mirror.publish(fedoraReleasePath, readTestdata(t, "fedora-44.primary.xml"), ".zst", 0)
		good := mirror.files[primary]
		// A second zstd frame after the genuine one: readers concatenate
		// frames, so one that ignored the declared size would decompress and
		// parse about 200 MB of packages.
		filler := `<package type="rpm"><name>filler</name><arch>noarch</arch><version ver="1" rel="1"/><summary>` + strings.Repeat("x", 4000) + "</summary></package>\n"
		tail := compress(t, ".zst", []byte(strings.Repeat(filler, 50_000)))
		mirror.intercept = func(writer http.ResponseWriter, request *http.Request) bool {
			if request.URL.Path != primary {
				return false
			}
			_, _ = writer.Write(append(append([]byte(nil), good...), tail...))
			return true
		}
		var heapBefore, heapAfter runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&heapBefore)
		_, err := OpenFedora(context.Background(), options, mirror.release())
		runtime.ReadMemStats(&heapAfter)
		if !errors.Is(err, ErrUnavailable) || !errors.Is(err, errTooLarge) || errors.Is(err, errChanged) {
			t.Fatalf("OpenFedora = %v, want ErrUnavailable wrapping errTooLarge alone", err)
		}
		if allocated := heapAfter.TotalAlloc - heapBefore.TotalAlloc; allocated > 64<<20 {
			t.Errorf("OpenFedora allocated %d MB reading a body it should have cut off at %d bytes", allocated>>20, len(good)+1)
		}
		if got := mirror.requestsFor(fedoraReleasePath + "/repodata/repomd.xml"); got != 1 {
			t.Errorf("repomd.xml was read %d times; a file too long is not asked for again", got)
		}
		if _, err := os.Stat(cachePath(options, fedoraTarget(mirror.release()))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("an index that was too long was cached: %v", err)
		}
	})
	t.Run("what it expands to", func(t *testing.T) {
		mirror := newFedoraMirror(t, ".gz")
		text := readTestdata(t, "fedora-44.primary.xml")
		mirror.publish(fedoraReleasePath, text, ".gz", int64(len(text)/2))
		if _, err := OpenFedora(context.Background(), fedoraOptions(t), mirror.release()); !errors.Is(err, ErrUnavailable) || !errors.Is(err, errTooLarge) {
			t.Errorf("OpenFedora = %v, want ErrUnavailable wrapping errTooLarge", err)
		}
	})
}

// TestOpenFedoraFetchesNothingRepomdCannotVouchFor: repomd.xml is read
// unsigned, so what it says is checked before anything is fetched on its
// word: a primary file below the repository, with a SHA-256 digest and a
// size.
func TestOpenFedoraFetchesNothingRepomdCannotVouchFor(t *testing.T) {
	const digest = "c48e47563bbf65b996c95caf4a608223f982c314cab637e6ab87dd1df67b9d26"
	testCases := map[string]string{
		"a path out of the repository": repomdListing("../../../etc/primary.xml.gz", digest, digest, 100, 1000),
		"another server":               repomdListing("https://elsewhere.example/primary.xml.gz", digest, digest, 100, 1000),
		"an absolute path":             repomdListing("/repodata/primary.xml.gz", digest, digest, 100, 1000),
		"no size":                      repomdListing("repodata/primary.xml.gz", digest, digest, 0, 1000),
		"a short digest":               repomdListing("repodata/primary.xml.gz", digest[:40], digest, 100, 1000),
		"a SHA-1 digest":               strings.Replace(repomdListing("repodata/primary.xml.gz", digest, digest, 100, 1000), `<checksum type="sha256">`+digest, `<checksum type="sha1">`+digest[:40], 1),
		"no primary metadata":          strings.Replace(repomdListing("repodata/primary.xml.gz", digest, digest, 100, 1000), `type="primary"`, `type="other"`, 1),
		"not XML":                      "InRelease\n",
	}
	for name, listing := range testCases {
		t.Run(name, func(t *testing.T) {
			mirror := newFedoraMirror(t, ".gz")
			mirror.files[fedoraReleasePath+"/repodata/repomd.xml"] = []byte(listing)
			_, err := OpenFedora(context.Background(), fedoraOptions(t), mirror.release())
			if !errors.Is(err, ErrUnavailable) {
				t.Errorf("OpenFedora = %v, want ErrUnavailable", err)
			}
			for path, count := range mirror.served {
				if !strings.HasSuffix(path, "/repomd.xml") {
					t.Errorf("%s was fetched %d times on the word of a listing that was refused", path, count)
				}
			}
		})
	}
}

func TestOpenFedoraStopsWhenTheContextEnds(t *testing.T) {
	mirror := newFedoraMirror(t, ".zst")
	ctx, cancel := context.WithCancel(context.Background())
	mirror.intercept = func(http.ResponseWriter, *http.Request) bool { cancel(); return false }
	if _, err := OpenFedora(ctx, fedoraOptions(t), mirror.release()); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled, not a claim that Fedora is down", err)
	}
}

func TestReducePrimary(t *testing.T) {
	document := func(packages ...string) string {
		return `<?xml version="1.0" encoding="UTF-8"?>
<metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm" packages="` + fmt.Sprint(len(packages)) + `">
` + strings.Join(packages, "\n") + "\n</metadata>\n"
	}
	rpm := func(name, arch, version, summary string) string {
		return `<package type="rpm"><name>` + name + `</name><arch>` + arch + `</arch>` + version + `<summary>` + summary + `</summary>` +
			`<format><rpm:provides><rpm:entry name="` + name + `"/></rpm:provides><file>/usr/bin/` + name + `</file></format></package>`
	}
	t.Run("what is kept", func(t *testing.T) {
		entries, err := reducePrimary(strings.NewReader(document(
			rpm("tool", "x86_64", `<version epoch="0" ver="1.10" rel="1.fc44"/>`, "the newer"),
			rpm("tool", "x86_64", `<version epoch="0" ver="1.9" rel="3.fc44"/>`, "the older, listed after it"),
			rpm("tool", "i686", `<version epoch="0" ver="2.0" rel="1.fc44"/>`, "another architecture"),
			rpm("tool-src", "src", `<version epoch="0" ver="1" rel="1"/>`, "a source package"),
			rpm("noversion", "noarch", ``, "no version"),
			rpm("spaced", "noarch", `<version epoch="3" ver="1" rel="1"/>`, "tabs\tand\nnewlines &amp; entities"),
			`<package type="srpm"><name>srpm</name><arch>noarch</arch><version ver="1" rel="1"/></package>`,
		)))
		if err != nil {
			t.Fatal(err)
		}
		want := []Entry{
			{Name: "spaced", Version: "3:1-1", Description: "tabs and newlines & entities"},
			{Name: "tool", Version: "1.10-1.fc44", Description: "the newer"},
		}
		if !slices.Equal(entries, want) {
			t.Errorf("entries =\n%+v\nwant\n%+v", entries, want)
		}
	})
	for name, text := range map[string]string{
		"cut short":         document(rpm("git", "x86_64", `<version ver="1" rel="1"/>`, "git"))[:120],
		"closed too often":  document(rpm("git", "x86_64", `<version ver="1" rel="1"/>`, "git")) + "</metadata>",
		"not XML":           "Package: git\nVersion: 1\n",
		"an unclosed quote": `<metadata><package type="rpm><name>git</name></package></metadata>`,
	} {
		t.Run(name, func(t *testing.T) {
			if entries, err := reducePrimary(strings.NewReader(text)); err == nil {
				t.Errorf("reducePrimary = %+v, want an error", entries)
			}
		})
	}
}

// TestRPMVerCmp is rpm's own table, from tests/rpmvercmp.at.
func TestRPMVerCmp(t *testing.T) {
	testCases := []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0}, {"1.0", "2.0", -1}, {"2.0", "1.0", 1},
		{"2.0.1", "2.0.1", 0}, {"2.0", "2.0.1", -1}, {"2.0.1", "2.0", 1},
		{"2.0.1a", "2.0.1a", 0}, {"2.0.1a", "2.0.1", 1}, {"2.0.1", "2.0.1a", -1},
		{"5.5p1", "5.5p1", 0}, {"5.5p1", "5.5p2", -1}, {"5.5p2", "5.5p1", 1},
		{"5.5p10", "5.5p10", 0}, {"5.5p1", "5.5p10", -1}, {"5.5p10", "5.5p1", 1},
		{"10xyz", "10.1xyz", -1}, {"10.1xyz", "10xyz", 1},
		{"xyz10", "xyz10", 0}, {"xyz10", "xyz10.1", -1}, {"xyz10.1", "xyz10", 1},
		{"xyz.4", "xyz.4", 0}, {"xyz.4", "8", -1}, {"8", "xyz.4", 1}, {"xyz.4", "2", -1}, {"2", "xyz.4", 1},
		{"5.5p2", "5.6p1", -1}, {"5.6p1", "5.5p2", 1}, {"5.6p1", "6.5p1", -1}, {"6.5p1", "5.6p1", 1},
		{"6.0.rc1", "6.0", 1}, {"6.0", "6.0.rc1", -1},
		{"10b2", "10a1", 1}, {"10a2", "10b2", -1},
		{"1.0aa", "1.0aa", 0}, {"1.0a", "1.0aa", -1}, {"1.0aa", "1.0a", 1},
		{"10.0001", "10.0001", 0}, {"10.0001", "10.1", 0}, {"10.1", "10.0001", 0},
		{"10.0001", "10.0039", -1}, {"10.0039", "10.0001", 1},
		{"4.999.9", "5.0", -1}, {"5.0", "4.999.9", 1},
		{"20101121", "20101121", 0}, {"20101121", "20101122", -1}, {"20101122", "20101121", 1},
		{"2_0", "2_0", 0}, {"2.0", "2_0", 0}, {"2_0", "2.0", 0},
		{"a", "a", 0}, {"a+", "a+", 0}, {"a+", "a_", 0}, {"a_", "a+", 0},
		{"+a", "+a", 0}, {"+a", "_a", 0}, {"_a", "+a", 0},
		{"+_", "+_", 0}, {"_+", "+_", 0}, {"_+", "_+", 0}, {"+", "_", 0}, {"_", "+", 0},
		{"1.0~rc1", "1.0~rc1", 0}, {"1.0~rc1", "1.0", -1}, {"1.0", "1.0~rc1", 1},
		{"1.0~rc1", "1.0~rc2", -1}, {"1.0~rc2", "1.0~rc1", 1},
		{"1.0~rc1~git123", "1.0~rc1~git123", 0}, {"1.0~rc1~git123", "1.0~rc1", -1}, {"1.0~rc1", "1.0~rc1~git123", 1},
		{"1.0^", "1.0^", 0}, {"1.0^", "1.0", 1}, {"1.0", "1.0^", -1},
		{"1.0^git1", "1.0^git1", 0}, {"1.0^git1", "1.0", 1}, {"1.0", "1.0^git1", -1},
		{"1.0^git1", "1.0^git2", -1}, {"1.0^git2", "1.0^git1", 1},
		{"1.0^git1", "1.01", -1}, {"1.01", "1.0^git1", 1},
		{"1.0^20160101", "1.0^20160101", 0}, {"1.0^20160101", "1.0.1", -1}, {"1.0.1", "1.0^20160101", 1},
		{"1.0^20160101^git1", "1.0^20160101^git1", 0}, {"1.0^20160102", "1.0^20160101^git1", 1}, {"1.0^20160101^git1", "1.0^20160102", -1},
		{"1.0~rc1^git1", "1.0~rc1^git1", 0}, {"1.0~rc1^git1", "1.0~rc1", 1}, {"1.0~rc1", "1.0~rc1^git1", -1},
		{"1.0^git1~pre", "1.0^git1~pre", 0}, {"1.0^git1", "1.0^git1~pre", 1}, {"1.0^git1~pre", "1.0^git1", -1},
	}
	for _, testCase := range testCases {
		if got := rpmvercmp(testCase.a, testCase.b); got != testCase.want {
			t.Errorf("rpmvercmp(%q, %q) = %d, want %d", testCase.a, testCase.b, got, testCase.want)
		}
	}
	// The epoch comes first, and an empty one is 0.
	if compareEVR("1", "1.0", "1", "0", "9.0", "1") != 1 || compareEVR("", "1.0", "1", "0", "1.0", "1") != 0 || compareEVR("0", "1.0", "2", "", "1.0", "10") != -1 {
		t.Error("compareEVR does not order by epoch, then version, then release")
	}
}
