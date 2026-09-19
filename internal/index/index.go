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

// sourceMaxAge is the same for a third-party repository, which a week does
// not suit: a vendor publishes when it likes, a PPA gains a Python version
// the week it is released, and the reason a week is cheap for the archive —
// 21 MB — does not apply to an index of tens of kilobytes. A day keeps a
// form that is opened twice in an afternoon off the network and still finds
// what was published yesterday.
const sourceMaxAge = 24 * time.Hour

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
	// Origin is the recipe source this package came from, or "" for the
	// release's own archive. It is not cached: which repository served a
	// file is a property of the index that was opened, not of the file.
	Origin string
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

// Index is the packages of one release, sorted by name, and of any sources
// merged into it.
type Index struct {
	// label is what Describe calls this index: the suite for a release's
	// archive, the recipe name for a source, and both for a union of them.
	label    string
	fetched  time.Time
	entries  []Entry
	lowered  []loweredEntry
	sections []SectionCount
	// searched names the repositories the index holds, the release's archive
	// first, and missing those it could not read: a source that answered
	// nothing, and one served from a cache because it could not be reached.
	// The warning on the last page says which of the two a name is absent
	// from, and the picker opens the index again rather than leaving a
	// repository out for the rest of the form.
	searched []string
	missing  []string
	// staleBecause is why a cache older than MaxAge is being used anyway.
	staleBecause error
	now          func() time.Time
}

// Sources returns the repositories the index holds and those it could not
// read, the release's archive first.
func (x *Index) Sources() (searched, missing []string) {
	return x.searched, x.missing
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
	return openTarget(ctx, options, archiveTarget(options))
}

// openTarget opens one repository: the release's archive, or a source the
// recipe adds beside it.
func openTarget(ctx context.Context, options Options, what target) (*Index, error) {
	if options.MaxAge == 0 {
		options.MaxAge = what.maxAge
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	cached := readCache(options, what)
	if cached != nil {
		fresh := options.Now().Sub(cached.fetched) < options.MaxAge
		if options.Offline || (fresh && !options.Refresh) {
			return cached, nil
		}
	}
	if options.Offline {
		return nil, fmt.Errorf("%w: nothing cached for %s, and this command does not fetch", ErrUnavailable, what.label)
	}
	entries, err := fetch(ctx, options, what)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if cached != nil {
			cached.staleBecause = err
			// Served from a cache because the repository could not be
			// reached: the same thing, to a reader, as one left out.
			cached.missing = []string{what.missingName()}
			return cached, nil
		}
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	stampOrigin(entries, what.origin)
	fetched := newIndex(what.label, options.Now(), entries, options.Now)
	// A cache that cannot be written costs the next run a fetch and this
	// one nothing.
	_ = writeCache(options, what, fetched)
	return fetched, nil
}

// stampOrigin marks entries as the source's. The archive's origin is "", so
// a release's own packages carry no source name and nothing shows one.
func stampOrigin(entries []Entry, origin string) {
	if origin == "" {
		return
	}
	for position := range entries {
		entries[position].Origin = origin
	}
}

// newIndex prepares entries, which must be sorted by name, for searching.
func newIndex(label string, fetched time.Time, entries []Entry, now func() time.Time) *Index {
	built := &Index{label: label, searched: []string{label}, fetched: fetched, entries: entries, now: now}
	built.lowered = make([]loweredEntry, len(entries))
	counts := map[string]int{}
	for position, entry := range entries {
		built.lowered[position] = loweredEntry{name: strings.ToLower(entry.Name), description: strings.ToLower(entry.Description)}
		// A stanza without a Section leaves one empty, which a vendor's
		// repository does where Ubuntu's archive does not. It is not a
		// section to narrow to: the chooser already offers "all sections",
		// and a second row of that name would filter by nothing.
		if entry.Section != "" {
			counts[entry.Section]++
		}
	}
	built.sections = sortedSections(counts)
	return built
}

// Len returns how many packages the index holds.
func (x *Index) Len() int { return len(x.entries) }

// Describe says what the index is in a few words, for the line above the
// results: "noble · 85,855 packages · fetched 2 days ago".
func (x *Index) Describe() string {
	description := fmt.Sprintf("%s · %s packages · fetched %s", x.label, groupThousands(len(x.entries)), ago(x.now().Sub(x.fetched)))
	if len(x.missing) > 0 {
		description += " · " + strings.Join(x.missing, ", ") + " not reachable"
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
func cachePath(options Options, what target) string {
	if options.CacheDir == "" {
		return ""
	}
	return filepath.Join(options.CacheDir, what.cacheName+"-"+distro.SupportedArch+".tsv.gz")
}

// removeQuietly deletes a file that is known to be useless. That it may
// fail is of no consequence: the next write replaces it.
func removeQuietly(path string) { _ = os.Remove(path) }
