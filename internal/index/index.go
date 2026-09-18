// Package index is the list of packages an Ubuntu release offers: fetched
// from the archive's own Packages files, reduced to what someone choosing
// packages reads, cached in the user's cache directory, and searched.
//
// It is advisory. The files are checked against the sizes and digests in the
// archive's Release file, which guards against a truncated or half-published
// download, but Release itself is read unsigned: a hostile mirror can make
// the index lie about what exists. It cannot make a build install anything,
// because build verifies every package against the signed archive and never
// reads this index.
package index

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"frostroot/internal/distro"
)

// DefaultMaxAge is how old a cached index may be before Open fetches again.
// The release pocket never changes and -updates adds names rarely; a week
// keeps a lab that runs init every day off the network.
const DefaultMaxAge = 7 * 24 * time.Hour

// ErrUnavailable means there is no index to be had: nothing cached, and the
// archive was not asked (Offline) or could not be read. It is not a failure
// of the form, which goes on without suggestions.
var ErrUnavailable = errors.New("package index not available")

// Entry is one package of a release.
type Entry struct {
	Name        string
	Version     string // the newest the fetched pockets offer
	Component   string // main, restricted, universe or multiverse
	Section     string // the archive section without its component: devel, python, libs
	Description string // the one-line description
}

// SectionCount is an archive section and how many packages it holds.
type SectionCount struct {
	Name  string
	Count int
}

// Options say which index to open and how.
type Options struct {
	Release  distro.Release // suite, archive URL and components
	Mirror   string         // replaces Release.ArchiveURL when not empty
	CacheDir string         // where index files are kept; see CacheDir
	MaxAge   time.Duration  // 0 means DefaultMaxAge
	Refresh  bool           // fetch even when the cache is fresh
	Offline  bool           // never fetch: the cache or ErrUnavailable
	// Client fetches; nil means one honoring the proxy environment and
	// verifying against RootCAs.
	Client  *http.Client
	RootCAs *x509.CertPool // nil means the host's roots; for an https mirror
	// Progress is told how much of the download is done. It is called from
	// the fetching goroutine, often.
	Progress func(doneBytes, totalBytes int64)
	Now      func() time.Time // nil means time.Now
}

// Index is the packages of one release, sorted by name.
type Index struct {
	suite    string
	fetched  time.Time
	entries  []Entry
	lowered  []loweredEntry
	sections []SectionCount
	// staleBecause is why a cache older than MaxAge is being used anyway.
	staleBecause error
	now          func() time.Time
}

// loweredEntry is what Search matches against.
type loweredEntry struct{ name, description string }

// CacheDir returns where index files belong for this user:
// $XDG_CACHE_HOME/frostroot/index, or the specification's default under the
// home directory. "" when neither is known, which Open treats as no cache.
func CacheDir(getenv func(string) string) string {
	if cacheHome := getenv("XDG_CACHE_HOME"); filepath.IsAbs(cacheHome) {
		return filepath.Join(cacheHome, "frostroot", "index")
	}
	if home := getenv("HOME"); filepath.IsAbs(home) {
		return filepath.Join(home, ".cache", "frostroot", "index")
	}
	return ""
}

// Open returns the index for a release: from the cache when it is fresh,
// from the archive otherwise, and from a stale cache when the archive cannot
// be reached. An error wraps ErrUnavailable unless ctx ended.
func Open(ctx context.Context, options Options) (*Index, error) {
	if options.MaxAge == 0 {
		options.MaxAge = DefaultMaxAge
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	cached := readCache(options)
	if cached != nil {
		fresh := options.Now().Sub(cached.fetched) < options.MaxAge
		if options.Offline || (fresh && !options.Refresh) {
			return cached, nil
		}
	}
	if options.Offline {
		return nil, fmt.Errorf("%w: nothing cached for %s, and this command does not fetch", ErrUnavailable, options.Release.Suite)
	}
	entries, err := fetch(ctx, options)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if cached != nil {
			cached.staleBecause = err
			return cached, nil
		}
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	fetched := newIndex(options.Release.Suite, options.Now(), entries, options.Now)
	// A cache that cannot be written costs the next run a fetch and this
	// one nothing.
	_ = writeCache(options, fetched)
	return fetched, nil
}

// newIndex prepares entries, which must be sorted by name, for searching.
func newIndex(suite string, fetched time.Time, entries []Entry, now func() time.Time) *Index {
	built := &Index{suite: suite, fetched: fetched, entries: entries, now: now}
	built.lowered = make([]loweredEntry, len(entries))
	counts := map[string]int{}
	for position, entry := range entries {
		built.lowered[position] = loweredEntry{name: strings.ToLower(entry.Name), description: strings.ToLower(entry.Description)}
		counts[entry.Section]++
	}
	built.sections = sortedSections(counts)
	return built
}

// Len returns how many packages the index holds.
func (x *Index) Len() int { return len(x.entries) }

// Describe says what the index is in a few words, for the line above the
// results: "noble · 85,855 packages · fetched 2 days ago".
func (x *Index) Describe() string {
	description := fmt.Sprintf("%s · %s packages · fetched %s", x.suite, groupThousands(len(x.entries)), ago(x.now().Sub(x.fetched)))
	if x.staleBecause != nil {
		description += " · archive not reachable"
	}
	return description
}

// ago renders an age the way people say it.
func ago(age time.Duration) string {
	switch {
	case age < time.Hour:
		return "just now"
	case age < 2*time.Hour:
		return "an hour ago"
	case age < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(age.Hours()))
	case age < 48*time.Hour:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", int(age.Hours()/24))
	}
}

// groupThousands renders 85855 as 85,855.
func groupThousands(number int) string {
	digits := fmt.Sprint(number)
	for position := len(digits) - 3; position > 0; position -= 3 {
		digits = digits[:position] + "," + digits[position:]
	}
	return digits
}

// cachePath is the file an index is kept in. The architecture is in the
// name so that a second one costs nothing the day there is one.
func cachePath(options Options) string {
	if options.CacheDir == "" {
		return ""
	}
	return filepath.Join(options.CacheDir, options.Release.Suite+"-"+distro.SupportedArch+".tsv.gz")
}

// removeQuietly deletes a file that is known to be useless. That it may
// fail is of no consequence: the next write replaces it.
func removeQuietly(path string) { _ = os.Remove(path) }
