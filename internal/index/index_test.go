package index

import (
	"bytes"
	"compress/gzip"
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

	"github.com/ulikunitz/xz"

	"frostroot/internal/distro"
	"frostroot/internal/index/indextest"
)

// testNow is when every test runs.
var testNow = time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)

// archive is a fake Ubuntu archive serving the excerpts in testdata, which
// are real paragraphs of noble's Packages files.
type archive struct {
	t      *testing.T
	server *httptest.Server
	mutex  sync.Mutex
	// files by URL path, such as /dists/noble/main/binary-amd64/Packages.xz
	files map[string][]byte
	// served counts requests by URL path.
	served map[string]int
	// intercept, when set, may answer a request itself.
	intercept func(writer http.ResponseWriter, request *http.Request) bool
}

// newArchive serves noble and noble-updates. extension chooses how the
// indexes are compressed: ".xz", ".gz" or "".
func newArchive(t *testing.T, extension string) *archive {
	t.Helper()
	served := &archive{t: t, files: map[string][]byte{}, served: map[string]int{}}
	served.publish("noble", extension, map[string]string{
		"main": "noble.main.Packages", "universe": "noble.universe.Packages", "multiverse": "noble.multiverse.Packages",
	})
	served.publish("noble-updates", extension, map[string]string{"main": "noble-updates.main.Packages"})
	served.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		served.mutex.Lock()
		served.served[request.URL.Path]++
		content, isServed := served.files[request.URL.Path]
		intercept := served.intercept
		served.mutex.Unlock()
		if intercept != nil && intercept(writer, request) {
			return
		}
		if !isServed {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(content)
	}))
	t.Cleanup(served.server.Close)
	return served
}

// publish puts a pocket's indexes and the InRelease that lists them in place.
func (a *archive) publish(pocket, extension string, excerptByComponent map[string]string) {
	a.t.Helper()
	var release strings.Builder
	release.WriteString("-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA512\n\nOrigin: Ubuntu\nSuite: " + pocket + "\nMD5Sum:\n 00000000000000000000000000000000 1 main/binary-amd64/Packages.xz\nSHA256:\n")
	for component, excerpt := range excerptByComponent {
		text, err := os.ReadFile(filepath.Join("testdata", excerpt))
		if err != nil {
			a.t.Fatal(err)
		}
		content := compress(a.t, extension, text)
		path := component + "/binary-amd64/Packages" + extension
		a.files["/dists/"+pocket+"/"+path] = content
		digest := sha256.Sum256(content)
		fmt.Fprintf(&release, " %s %d %s\n", hex.EncodeToString(digest[:]), len(content), path)
	}
	release.WriteString("-----BEGIN PGP SIGNATURE-----\n\niQIzBAEBCgAdFiEE\n-----END PGP SIGNATURE-----\n")
	a.files["/dists/"+pocket+"/InRelease"] = []byte(release.String())
}

func compress(t *testing.T, extension string, text []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	switch extension {
	case ".xz":
		writer, err := xz.NewWriter(&buffer)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(text); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	case ".gz":
		writer := gzip.NewWriter(&buffer)
		if _, err := writer.Write(text); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	default:
		buffer.Write(text)
	}
	return buffer.Bytes()
}

// requests returns how many requests the archive has answered.
func (a *archive) requests() int {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	total := 0
	for _, count := range a.served {
		total += count
	}
	return total
}

// optionsFor opens noble from served, caching in a directory of the test's.
func optionsFor(t *testing.T, served *archive) Options {
	t.Helper()
	return Options{
		Release:  distro.Release{Suite: "noble", ArchiveURL: served.server.URL, Components: []string{"main", "restricted", "universe", "multiverse"}},
		CacheDir: t.TempDir(),
		Now:      func() time.Time { return testNow },
	}
}

func names(entries []Entry) []string {
	var result []string
	for _, entry := range entries {
		result = append(result, entry.Name)
	}
	return result
}

func TestOpenFetchesReducesAndCaches(t *testing.T) {
	for _, extension := range []string{".xz", ".gz", ""} {
		t.Run("Packages"+extension, func(t *testing.T) {
			served := newArchive(t, extension)
			options := optionsFor(t, served)
			var lastDone, lastTotal int64
			options.Progress = func(done, total int64) { lastDone, lastTotal = done, total }

			opened, err := Open(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			if opened.Len() != 17 {
				t.Errorf("Len = %d, want the 17 packages of the excerpts: %v", opened.Len(), names(opened.entries))
			}
			if lastTotal == 0 || lastDone != lastTotal {
				t.Errorf("progress ended at %d of %d, want all of a known total", lastDone, lastTotal)
			}
			matches, _ := opened.Search("git", "", 10)
			if len(matches) == 0 || matches[0].Name != "git" || matches[0].Version != "1:2.43.0-1ubuntu7.3" {
				t.Errorf("git = %+v, want the version -updates has", matches)
			}
			unrar, _ := opened.Search("unrar", "", 1)
			if len(unrar) != 1 || unrar[0].Component != "multiverse" || unrar[0].Section != "utils" {
				t.Errorf("unrar = %+v, want multiverse, and the section without its component", unrar)
			}
			if got := opened.Describe(); got != "noble · 17 packages · fetched just now" {
				t.Errorf("Describe = %q", got)
			}

			// The second Open reads the cache and asks the archive nothing.
			before := served.requests()
			again, err := Open(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			if served.requests() != before {
				t.Errorf("a fresh cache still cost %d requests", served.requests()-before)
			}
			if !slices.Equal(again.entries, opened.entries) {
				t.Errorf("the cached index differs from the fetched one:\n%v\n%v", again.entries, opened.entries)
			}
		})
	}
}

func TestOpenRefetchesAStaleCacheAndFallsBackToIt(t *testing.T) {
	served := newArchive(t, ".gz")
	options := optionsFor(t, served)
	if _, err := Open(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	before := served.requests()

	// Six days on, the cache serves; eight days on, the archive is asked.
	options.Now = func() time.Time { return testNow.Add(6 * 24 * time.Hour) }
	if _, err := Open(context.Background(), options); err != nil || served.requests() != before {
		t.Fatalf("a six-day-old cache: err %v, %d new requests", err, served.requests()-before)
	}
	options.Refresh = true
	if _, err := Open(context.Background(), options); err != nil || served.requests() == before {
		t.Fatalf("--refresh-index: err %v, %d new requests, want a fetch", err, served.requests()-before)
	}
	options.Refresh = false
	before = served.requests()
	options.Now = func() time.Time { return testNow.Add(14 * 24 * time.Hour) }
	refreshed, err := Open(context.Background(), options)
	if err != nil || served.requests() == before {
		t.Fatalf("an old cache: err %v, %d new requests, want a fetch", err, served.requests()-before)
	}
	if got := refreshed.Describe(); !strings.HasSuffix(got, "fetched just now") {
		t.Errorf("Describe = %q after a refetch", got)
	}

	// With the archive gone, the old cache is better than nothing, and says so.
	served.server.Close()
	options.Now = func() time.Time { return testNow.Add(30 * 24 * time.Hour) }
	stale, err := Open(context.Background(), options)
	if err != nil {
		t.Fatalf("a stale cache and no archive: %v", err)
	}
	if got := stale.Describe(); got != "noble · 17 packages · fetched 16 days ago · archive not reachable" {
		t.Errorf("Describe = %q", got)
	}
}

func TestOpenWithoutCacheOrArchiveIsUnavailable(t *testing.T) {
	served := newArchive(t, ".gz")
	options := optionsFor(t, served)
	served.server.Close()
	if _, err := Open(context.Background(), options); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestOpenOfflineNeverFetches(t *testing.T) {
	served := newArchive(t, ".gz")
	options := optionsFor(t, served)
	options.Offline = true
	if _, err := Open(context.Background(), options); !errors.Is(err, ErrUnavailable) {
		t.Errorf("offline without a cache: err = %v, want ErrUnavailable", err)
	}
	options.Offline = false
	if _, err := Open(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	before := served.requests()
	options.Offline = true
	options.Now = func() time.Time { return testNow.Add(365 * 24 * time.Hour) }
	if _, err := Open(context.Background(), options); err != nil || served.requests() != before {
		t.Errorf("offline with a year-old cache: err %v, %d requests; want the cache and none", err, served.requests()-before)
	}
}

func TestOpenDeletesACorruptCache(t *testing.T) {
	served := newArchive(t, ".gz")
	options := optionsFor(t, served)
	path := cachePath(options, archiveTarget(options))
	for name, content := range map[string][]byte{
		"not gzip":       []byte("Package: git\n"),
		"another format": compress(t, ".gz", []byte("frostroot-index 0\tnoble\n")),
		"unsorted":       compress(t, ".gz", []byte(headerFor(archiveTarget(options), testNow).line()+"\nzip\t1\tmain\tutils\tArchiver\ngit\t1\tmain\tvcs\tfast\n")),
		"short entry":    compress(t, ".gz", []byte(headerFor(archiveTarget(options), testNow).line()+"\ngit\t1\tmain\n")),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, content, 0o644); err != nil {
				t.Fatal(err)
			}
			before := served.requests()
			opened, err := Open(context.Background(), options)
			if err != nil || opened.Len() != 17 {
				t.Fatalf("Open = %v, %v; want the fetched index", opened, err)
			}
			if served.requests() == before {
				t.Error("a corrupt cache was trusted")
			}
		})
	}
	// Offline there is nothing to replace it with, and it still goes.
	if err := os.WriteFile(path, []byte("rubbish"), 0o644); err != nil {
		t.Fatal(err)
	}
	options.Offline = true
	if _, err := Open(context.Background(), options); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the corrupt file is still there: %v", err)
	}
}

func TestOpenIgnoresACacheOfAnotherSource(t *testing.T) {
	served := newArchive(t, ".gz")
	options := optionsFor(t, served)
	if _, err := Open(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	mirror := newArchive(t, ".gz")
	for name, change := range map[string]func(*Options){
		"another mirror":   func(o *Options) { o.Mirror = mirror.server.URL },
		"other components": func(o *Options) { o.Release.Components = []string{"main"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := options
			change(&changed)
			before := served.requests() + mirror.requests()
			if _, err := Open(context.Background(), changed); err != nil {
				t.Fatal(err)
			}
			if served.requests()+mirror.requests() == before {
				t.Error("the cache of another source was used")
			}
		})
	}
}

func TestOpenTriesAChangedPocketOnceMore(t *testing.T) {
	served := newArchive(t, ".gz")
	options := optionsFor(t, served)
	universe := "/dists/noble/universe/binary-amd64/Packages.gz"
	good := served.files[universe]

	// The first answer is cut short, as a mirror mid-publication or a
	// dropped connection would leave it; the second is whole.
	failures := 1
	served.intercept = func(writer http.ResponseWriter, request *http.Request) bool {
		served.mutex.Lock()
		defer served.mutex.Unlock()
		if request.URL.Path != universe || failures == 0 {
			return false
		}
		failures--
		_, _ = writer.Write(good[:len(good)/2])
		return true
	}
	opened, err := Open(context.Background(), options)
	if err != nil || opened.Len() != 17 {
		t.Fatalf("Open = %v, %v; want the index after a second try", opened, err)
	}

	// A file that stays wrong is given up on, and nothing is cached.
	options.CacheDir = t.TempDir()
	served.intercept = func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != universe {
			return false
		}
		_, _ = writer.Write(compress(t, ".gz", []byte("Package: evil\nVersion: 1\n")))
		return true
	}
	if _, err := Open(context.Background(), options); !errors.Is(err, ErrUnavailable) || !errors.Is(err, errChanged) {
		t.Errorf("err = %v, want ErrUnavailable wrapping the mismatch", err)
	}
	if _, err := os.Stat(cachePath(options, archiveTarget(options))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an index that failed its check was cached: %v", err)
	}
}

func TestOpenToleratesAMirrorWithoutUpdates(t *testing.T) {
	served := newArchive(t, ".gz")
	delete(served.files, "/dists/noble-updates/InRelease")
	opened, err := Open(context.Background(), optionsFor(t, served))
	if err != nil {
		t.Fatal(err)
	}
	git, _ := opened.Search("git", "", 1)
	if len(git) != 1 || git[0].Version != "1:2.43.0-1ubuntu7" {
		t.Errorf("git = %+v, want the release pocket's version", git)
	}
}

func TestOpenReadsReleaseWhenThereIsNoInRelease(t *testing.T) {
	served := newArchive(t, ".gz")
	for _, pocket := range []string{"noble", "noble-updates"} {
		served.files["/dists/"+pocket+"/Release"] = served.files["/dists/"+pocket+"/InRelease"]
		delete(served.files, "/dists/"+pocket+"/InRelease")
	}
	if opened, err := Open(context.Background(), optionsFor(t, served)); err != nil || opened.Len() != 17 {
		t.Errorf("Open = %v, %v", opened, err)
	}
}

func TestOpenStopsWhenTheContextEnds(t *testing.T) {
	served := newArchive(t, ".gz")
	ctx, cancel := context.WithCancel(context.Background())
	served.intercept = func(http.ResponseWriter, *http.Request) bool { cancel(); return false }
	if _, err := Open(ctx, optionsFor(t, served)); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled, not a claim that the archive is down", err)
	}
}

func TestCacheDir(t *testing.T) {
	environment := func(variables map[string]string) func(string) string {
		return func(name string) string { return variables[name] }
	}
	tests := []struct {
		variables map[string]string
		want      string
	}{
		{map[string]string{"XDG_CACHE_HOME": "/x/cache", "HOME": "/home/u"}, "/x/cache/frostroot/index"},
		{map[string]string{"XDG_CACHE_HOME": "relative", "HOME": "/home/u"}, "/home/u/.cache/frostroot/index"},
		{map[string]string{"HOME": "/home/u"}, "/home/u/.cache/frostroot/index"},
		{map[string]string{}, ""},
	}
	for _, test := range tests {
		if got := CacheDir(environment(test.variables)); got != test.want {
			t.Errorf("CacheDir(%v) = %q, want %q", test.variables, got, test.want)
		}
	}
	// Without a cache directory Open still works; it just fetches each time.
	served := newArchive(t, ".gz")
	options := optionsFor(t, served)
	options.CacheDir = ""
	if opened, err := Open(context.Background(), options); err != nil || opened.Len() != 17 {
		t.Errorf("Open without a cache directory = %v, %v", opened, err)
	}
}

// TestOpenRefusesAnIndexLongerThanReleaseDeclares: the size Release lists
// bounds the download before anything is parsed. A body that keeps coming
// past it is refused at that byte, however much it would expand to, so a
// repository cannot end the process where it should only fail its own
// source. The tail here would expand to about 200 MB of stanzas if it were
// ever read; the read stops at the declared size plus one byte.
func TestOpenRefusesAnIndexLongerThanReleaseDeclares(t *testing.T) {
	served := newArchive(t, ".gz")
	options := optionsFor(t, served)
	universe := "/dists/noble/universe/binary-amd64/Packages.gz"
	good := served.files[universe]
	// A gzip member of one hugely repetitive stanza, appended after the
	// genuine member: gzip readers concatenate members, so a reader that
	// ignored the declared size would decompress and keep it all.
	stanza := "Package: filler\nVersion: 1\nDescription: " + strings.Repeat("x", 4000) + "\n\n"
	tail := compress(t, ".gz", []byte(strings.Repeat(stanza, 20_000)))
	served.intercept = func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != universe {
			return false
		}
		_, _ = writer.Write(append(append([]byte(nil), good...), tail...))
		return true
	}

	var heapBefore, heapAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&heapBefore)
	_, err := Open(context.Background(), options)
	runtime.ReadMemStats(&heapAfter)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, errTooLarge) {
		t.Fatalf("Open = %v, want ErrUnavailable wrapping errTooLarge", err)
	}
	if errors.Is(err, errChanged) {
		t.Errorf("err = %v; a body that is too long is not a body that changed", err)
	}
	// The tail is 80 MB decompressed; a bounded read allocates a small
	// multiple of the declared size and no more.
	if allocated := heapAfter.TotalAlloc - heapBefore.TotalAlloc; allocated > 32<<20 {
		t.Errorf("Open allocated %d MB reading a body it should have cut off at %d bytes", allocated>>20, len(good)+1)
	}
	if _, err := os.Stat(cachePath(options, archiveTarget(options))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an index that was too long was cached: %v", err)
	}
}

// A source that overruns its declared size fails alone, and is not asked
// again: a changed file is worth a second request, because the mirror may
// have finished publishing, but a file that is too long stays too long.
func TestOpenSourceThatOverrunsItsSizeFailsAlone(t *testing.T) {
	served := indextest.Serve(t, "noble", dockerPackages)
	served.Overrun = strings.Repeat("Package: filler\nVersion: 1\n\n", 100_000)
	docker := Source{Name: "docker", URL: served.URL, Suite: "noble", Components: []string{"main"}}

	_, err := OpenSource(context.Background(), sourceOptionsFor(t), docker)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, errTooLarge) {
		t.Fatalf("OpenSource = %v, want ErrUnavailable wrapping errTooLarge", err)
	}
	if served.Requests() != 2 {
		t.Errorf("requests = %d, want the InRelease and one Packages file: a body that is too long is not fetched twice", served.Requests())
	}
}

// TestOpenKeepsAFoldedFieldOnOneCacheLine: deb.ReadStanzas joins a
// continuation line to its field with a newline, and a vendor's stanza may
// fold Section or Version that way. The cache is a line per package, so a
// newline inside a field wrote a line the next run could not parse, which
// deleted the cache and fetched the repository again, for ever.
func TestOpenKeepsAFoldedFieldOnOneCacheLine(t *testing.T) {
	served := indextest.Serve(t, "noble", []indextest.Package{
		{Name: "folded", Version: "1.0", Section: "admin\n extra", Description: "a stanza whose section runs over two lines"},
	})
	options := Options{
		Release:  distro.Release{Suite: "noble", ArchiveURL: served.URL, Components: []string{"main"}},
		CacheDir: t.TempDir(),
		Now:      func() time.Time { return testNow },
	}
	opened, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if entry, found := opened.Lookup("folded"); !found || entry.Section != "admin extra" {
		t.Errorf("Lookup = %+v, %v; want the folded section on one line", entry, found)
	}
	before := served.Requests()
	again, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if served.Requests() != before {
		t.Errorf("the cache did not read back: %d more requests", served.Requests()-before)
	}
	if entry, found := again.Lookup("folded"); !found || entry.Section != "admin extra" {
		t.Errorf("from the cache, Lookup = %+v, %v", entry, found)
	}
}
