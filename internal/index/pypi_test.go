package index

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// simpleIndex serves the excerpt in testdata as PyPI's simple index, gzipped
// the way PyPI serves it.
type simpleIndex struct {
	URL      string
	server   *httptest.Server
	requests atomic.Int64
	// answer, when set, replies instead of the excerpt.
	answer func(writer http.ResponseWriter) bool
}

func newSimpleIndex(t *testing.T) *simpleIndex {
	t.Helper()
	document, err := os.ReadFile(filepath.Join("testdata", "pypi-simple.json"))
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(document); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	index := &simpleIndex{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		index.requests.Add(1)
		if index.answer != nil && index.answer(response) {
			return
		}
		if !strings.Contains(request.Header.Get("Accept"), "vnd.pypi.simple") {
			http.Error(response, "the JSON simple index was not asked for", http.StatusNotAcceptable)
			return
		}
		if strings.Contains(request.Header.Get("Accept-Encoding"), "gzip") {
			response.Header().Set("Content-Encoding", "gzip")
			_, _ = response.Write(compressed.Bytes())
			return
		}
		_, _ = response.Write(document)
	}))
	t.Cleanup(server.Close)
	index.URL = server.URL
	index.server = server
	return index
}

// pypiOptionsFor opens served, caching in a directory of the test's.
func pypiOptionsFor(t *testing.T, served *simpleIndex) Options {
	t.Helper()
	return Options{Mirror: served.URL, CacheDir: t.TempDir(), Now: func() time.Time { return testNow }}
}

func projectNames(projects []Project) []string {
	var names []string
	for _, project := range projects {
		names = append(names, project.Name)
	}
	return names
}

func TestOpenPyPIFetchesAndCaches(t *testing.T) {
	served := newSimpleIndex(t)
	options := pypiOptionsFor(t, served)
	var lastDone, lastTotal int64
	options.Progress = func(done, total int64) { lastDone, lastTotal = done, total }

	opened, err := OpenPyPI(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Len() != 15 {
		t.Errorf("Len = %d, want the 15 of the excerpt: %v", opened.Len(), projectNames(opened.projects))
	}
	if lastDone == 0 || lastDone != lastTotal {
		t.Errorf("progress ended at %d of %d, want all of a known total", lastDone, lastTotal)
	}
	if got := opened.Describe(); got != "PyPI · 15 projects · fetched just now" {
		t.Errorf("Describe = %q", got)
	}

	before := served.requests.Load()
	again, err := OpenPyPI(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if served.requests.Load() != before {
		t.Errorf("a fresh cache still cost %d requests", served.requests.Load()-before)
	}
	if !slices.Equal(projectNames(again.projects), projectNames(opened.projects)) {
		t.Errorf("the cached index differs:\n%v\n%v", projectNames(again.projects), projectNames(opened.projects))
	}
	// The cache keeps PyPI's own spelling, which is what the picker shows.
	if !slices.Contains(projectNames(again.projects), "Flask-SQLAlchemy") {
		t.Errorf("the cache lost PyPI's spelling: %v", projectNames(again.projects))
	}
}

func TestPyPILookupNormalizes(t *testing.T) {
	opened := openPyPIExcerpt(t)
	// PEP 503: these are all one project, and a recipe may spell it any way.
	for _, spelling := range []string{"Flask-SQLAlchemy", "flask-sqlalchemy", "Flask_SQLAlchemy", "FLASK.SQLALCHEMY", "flask--sqlalchemy"} {
		found, isThere := opened.Lookup(spelling)
		if !isThere || found.Name != "Flask-SQLAlchemy" {
			t.Errorf("Lookup(%q) = %+v, %v; want PyPI's own spelling", spelling, found, isThere)
		}
	}
	for _, spelling := range []string{"zope.interface", "zope-interface", "zope_interface"} {
		if !opened.Has(spelling) {
			t.Errorf("Has(%q) = false", spelling)
		}
	}
	for _, missing := range []string{"reqeusts", "flask-sqlalchemyx", "", "PIL"} {
		if opened.Has(missing) {
			t.Errorf("Has(%q) = true", missing)
		}
	}
}

func TestPyPISearchRanks(t *testing.T) {
	opened := openPyPIExcerpt(t)
	tests := []struct {
		name  string
		query string
		limit int
		want  []string
		total int
	}{
		{
			// requests-oauthlib and requests-toolbelt are both 17 characters,
			// so the length tiebreak leaves them in name order.
			name: "exact, then prefix shortest first", query: "requests", limit: 10,
			want:  []string{"requests", "requests-cache", "requests-oauthlib", "requests-toolbelt"},
			total: 4,
		},
		{name: "case and separators do not matter", query: "FLASK_SQL", limit: 10, want: []string{"Flask-SQLAlchemy"}, total: 1},
		{name: "a substring in the middle", query: "learn", limit: 10, want: []string{"scikit-learn"}, total: 1},
		{name: "the limit cuts the list, not the count", query: "requests", limit: 2, want: []string{"requests", "requests-cache"}, total: 4},
		{name: "nothing matches", query: "reqeusts", limit: 10, want: nil, total: 0},
		{name: "an empty query lists everything", query: "", limit: 3, want: []string{"Django", "Flask", "Flask-SQLAlchemy"}, total: 15},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matches, total := opened.Search(test.query, "", test.limit)
			if got := projectNames(matches); !slices.Equal(got, test.want) || total != test.total {
				t.Errorf("Search(%q) = %v of %d, want %v of %d", test.query, got, total, test.want, test.total)
			}
		})
	}
}

func TestPyPINearest(t *testing.T) {
	opened := openPyPIExcerpt(t)
	for name, want := range map[string][]string{
		"reqeusts":   {"requests"},
		"nunpy":      {"numpy"},
		"djano":      {"Django"},
		"pillow":     {"pillow"},
		"kubernetes": nil, // a different project, not a slip
	} {
		if got := opened.Nearest(name, 3); !slices.Equal(got, want) {
			t.Errorf("Nearest(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestOpenPyPIFallsBackAndRefuses(t *testing.T) {
	served := newSimpleIndex(t)
	options := pypiOptionsFor(t, served)
	if _, err := OpenPyPI(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	// A week on, with PyPI gone, the stale cache serves and says so.
	served.answer = func(writer http.ResponseWriter) bool {
		http.Error(writer, "down", http.StatusBadGateway)
		return true
	}
	options.Now = func() time.Time { return testNow.Add(30 * 24 * time.Hour) }
	stale, err := OpenPyPI(context.Background(), options)
	if err != nil {
		t.Fatalf("a stale cache and no PyPI: %v", err)
	}
	if got := stale.Describe(); !strings.HasSuffix(got, "· PyPI not reachable") {
		t.Errorf("Describe = %q", got)
	}
	// With no cache at all there is nothing to fall back to.
	options.CacheDir = t.TempDir()
	if _, err := OpenPyPI(context.Background(), options); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
	// Offline never asks.
	options.Offline = true
	before := served.requests.Load()
	if _, err := OpenPyPI(context.Background(), options); !errors.Is(err, ErrUnavailable) {
		t.Errorf("offline err = %v, want ErrUnavailable", err)
	}
	if served.requests.Load() != before {
		t.Error("offline asked PyPI")
	}
}

func TestOpenPyPIDeletesACorruptCacheAndIgnoresAnotherIndex(t *testing.T) {
	served := newSimpleIndex(t)
	options := pypiOptionsFor(t, served)
	path := pypiCachePath(options)
	header := strings.Join([]string{pypiCacheFormat, served.URL + "/", testNow.UTC().Format(time.RFC3339)}, "\t")
	for name, content := range map[string][]byte{
		"not gzip":       []byte("requests\n"),
		"another format": gzipped(t, "frostroot-pypi 0\tx\tx\nrequests\n"),
		"unsorted":       gzipped(t, header+"\nrequests\nDjango\n"),
		"no projects":    gzipped(t, header+"\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, content, 0o644); err != nil {
				t.Fatal(err)
			}
			before := served.requests.Load()
			opened, err := OpenPyPI(context.Background(), options)
			if err != nil || opened.Len() != 15 {
				t.Fatalf("OpenPyPI = %v, %v", opened, err)
			}
			if served.requests.Load() == before {
				t.Error("a corrupt cache was trusted")
			}
		})
	}
	// A cache of another index is left alone and not used.
	if _, err := OpenPyPI(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	elsewhere := newSimpleIndex(t)
	changed := options
	changed.Mirror = elsewhere.URL
	if _, err := OpenPyPI(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	if elsewhere.requests.Load() == 0 {
		t.Error("the cache of another index was used")
	}
}

func TestOpenPyPIStopsWhenTheContextEnds(t *testing.T) {
	served := newSimpleIndex(t)
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel while the request is in flight and hold the response back, so
	// that the cancellation is what ends the fetch. Returning the excerpt at
	// once would let the whole 573-byte body arrive first.
	served.answer = func(http.ResponseWriter) bool {
		cancel()
		time.Sleep(2 * time.Second)
		return true
	}
	if _, err := OpenPyPI(ctx, pypiOptionsFor(t, served)); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled, not a claim that PyPI is down", err)
	}
}

// openPyPIExcerpt returns the index of the testdata excerpt.
func openPyPIExcerpt(t *testing.T) *PyPIIndex {
	t.Helper()
	opened, err := OpenPyPI(context.Background(), pypiOptionsFor(t, newSimpleIndex(t)))
	if err != nil {
		t.Fatal(err)
	}
	return opened
}

func gzipped(t *testing.T, text string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
