package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Source is a third-party apt repository to index beside the release's own
// archive: what a recipe's [[sources]] entry resolves to, with {suite}
// already replaced. The recipe carries the same four things, plus the key
// that build verifies it with; the index verifies nothing and so needs no
// key.
type Source struct {
	Name       string // the recipe source name: what Describe and a row call it
	URL        string // base URL, such as https://download.docker.com/linux/ubuntu
	Suite      string // resolved: the release's code name when the source names none
	Components []string
}

// target is one repository to index. Options say how to fetch and where to
// cache; a target says what.
type target struct {
	label      string   // what Describe calls it
	origin     string   // what its entries are stamped with; "" for the archive
	cacheName  string   // the cache file's name, without the architecture
	baseURL    string   // trimmed of its trailing slash
	suite      string   //
	components []string //
	pockets    []string // the suites under dists/ to read, in order
}

// archiveTarget is the release's own archive, or the mirror standing in for
// it. Its cache file is named after the suite alone, as it always was.
func archiveTarget(options Options) target {
	baseURL := options.Release.ArchiveURL
	if options.Mirror != "" {
		baseURL = options.Mirror
	}
	suite := options.Release.Suite
	return target{
		label:      suite,
		cacheName:  suite,
		baseURL:    strings.TrimRight(baseURL, "/"),
		suite:      suite,
		components: options.Release.Components,
		// -security is left out: everything in it is copied to -updates.
		pockets: []string{suite, suite + "-updates"},
	}
}

// sourceTarget is one third-party repository. --mirror is an Ubuntu archive
// and says nothing about a source, so it is not applied here. There is no
// -updates pocket: that scheme is Ubuntu's, and asking a vendor's repository
// for one buys a round trip and a 404.
func sourceTarget(source Source) target {
	baseURL := strings.TrimRight(source.URL, "/")
	return target{
		label:      source.Name,
		origin:     source.Name,
		cacheName:  sourceCacheName(source, baseURL),
		baseURL:    baseURL,
		suite:      source.Suite,
		components: source.Components,
		pockets:    []string{source.Suite},
	}
}

// sourceCacheName names a source's cache file: its own name, so a person
// can see what the cache holds, and a digest of what it points at, so two
// recipes that call different repositories "docker" do not overwrite each
// other. A source's suite is often the release's, so the suite alone —
// which is all an archive needs — would collide with the archive itself.
func sourceCacheName(source Source, baseURL string) string {
	digest := sha256.Sum256([]byte(baseURL + "\n" + source.Suite + "\n" + strings.Join(source.Components, ",")))
	return "source-" + safeFileName(source.Name) + "-" + hex.EncodeToString(digest[:])[:8]
}

// safeFileName keeps a name to what a file name may hold. Source names are
// validated before they reach a recipe, so this only guards against a caller
// that has not validated one.
func safeFileName(name string) string {
	return strings.Map(func(letter rune) rune {
		switch {
		case letter >= 'a' && letter <= 'z', letter >= 'A' && letter <= 'Z', letter >= '0' && letter <= '9':
			return letter
		case letter == '.' || letter == '-' || letter == '_':
			return letter
		}
		return '_'
	}, name)
}

// OpenSource returns the index of one third-party repository, cached beside
// the archive's and fetched the same way. A source whose repository cannot
// be read is an error and never a failure of the form: the caller drops it
// and searches the rest.
func OpenSource(ctx context.Context, options Options, source Source) (*Index, error) {
	return openTarget(ctx, options, sourceTarget(source))
}

// Union merges indexes into the one list a picker searches: the archive
// first, then each source in the order the recipe names them. A name two of
// them have is shown as the later one's, which is the repository a person
// added deliberately; apt itself chooses by version, so the version shown is
// not a promise of the version installed. unreachable names the sources
// whose index could not be opened, so that a search says what it is missing
// rather than quietly lacking it.
func Union(parts []*Index, unreachable []string) *Index {
	present := make([]*Index, 0, len(parts))
	for _, part := range parts {
		if part != nil {
			present = append(present, part)
		}
	}
	if len(present) == 0 {
		return nil
	}
	if len(present) == 1 && len(unreachable) == 0 {
		return present[0]
	}
	byName := map[string]Entry{}
	labels := make([]string, 0, len(present))
	oldest := present[0].fetched
	var stale error
	for _, part := range present {
		labels = append(labels, part.label)
		if part.fetched.Before(oldest) {
			oldest = part.fetched
		}
		if part.staleBecause != nil {
			stale = part.staleBecause
		}
		for _, entry := range part.entries {
			byName[entry.Name] = entry
		}
	}
	entries := make([]Entry, 0, len(byName))
	for _, entry := range byName {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	merged := newIndex(unionLabel(labels, unreachable), oldest, entries, present[0].now)
	merged.staleBecause = stale
	return merged
}

// unionLabel is what the line above the results calls a merged index:
// "noble + docker, kitware", and the sources it could not reach after it.
func unionLabel(labels, unreachable []string) string {
	label := labels[0]
	if len(labels) > 1 {
		label += " + " + strings.Join(labels[1:], ", ")
	}
	if len(unreachable) > 0 {
		label += " (" + strings.Join(unreachable, ", ") + " not reachable)"
	}
	return label
}
