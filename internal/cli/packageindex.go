package cli

import (
	"context"
	"flag"
	"sync"

	"frostroot/internal/distro"
	"frostroot/internal/form"
	"frostroot/internal/index"
)

// indexFlags are what init, edit and capture take for the package index
// their form searches.
type indexFlags struct {
	mirror   string
	caBundle string
	refresh  bool
}

// addIndexFlags registers the index flags on a command's flag set.
func addIndexFlags(flags *flag.FlagSet) *indexFlags {
	parsed := &indexFlags{}
	flags.StringVar(&parsed.mirror, "mirror", "", "fetch the package index from this archive `URL` instead of Ubuntu's")
	flags.StringVar(&parsed.caBundle, "ca-bundle", "", "trust the certificate authorities in this PEM `FILE` when the mirror is https, as a network that inspects TLS needs")
	flags.BoolVar(&parsed.refresh, "refresh-index", false, "fetch the package index again even when the cached one is fresh")
	return parsed
}

// indexUsageText is what the usage of a command with the form says about
// the index; the flags themselves are listed after it.
const indexUsageText = `The form searches the release's package index, fetched once and kept under
$XDG_CACHE_HOME/frostroot/index (or ~/.cache) for a week. It only suggests
names: build checks every package against the signed archive. --plain never
fetches; it checks names against the cached index when there is one.

`

// packageIndexes opens package indexes for one run of the recipe form: each
// release once, however often the form asks, because the picker opens it
// when it is reached and the summary asks again for its warning.
type packageIndexes struct {
	open    func(ctx context.Context, options index.Options) (*index.Index, error)
	options index.Options // what every release shares: mirror, cache, trust

	mutex  sync.Mutex
	opened map[string]form.PackageIndex
}

// packageIndexes returns the indexes for this run, or false when the flags
// cannot be honored, which has been reported.
func (a *App) packageIndexes(parsed *indexFlags) (*packageIndexes, bool) {
	options := index.Options{Mirror: parsed.mirror, CacheDir: index.CacheDir(a.Getenv), Refresh: parsed.refresh, Client: a.IndexClient}
	rootCAs, ok := a.trustPool(parsed.caBundle)
	if !ok {
		return nil, false
	}
	options.RootCAs = rootCAs
	return &packageIndexes{open: index.Open, options: options, opened: map[string]form.PackageIndex{}}, true
}

// Open is the form.IndexOpener of this run.
func (p *packageIndexes) Open(ctx context.Context, releaseVersion string, progress func(doneBytes, totalBytes int64)) (form.PackageIndex, error) {
	return p.get(ctx, releaseVersion, progress, false)
}

// Known returns the index of a release without ever fetching it: the one
// the form opened, or the cached one. nil when there is neither, and then
// nothing is known about any name.
func (p *packageIndexes) Known(releaseVersion string) form.PackageIndex {
	if p == nil {
		return nil
	}
	known, err := p.get(context.Background(), releaseVersion, nil, true)
	if err != nil {
		return nil
	}
	return known
}

func (p *packageIndexes) get(ctx context.Context, releaseVersion string, progress func(int64, int64), offline bool) (form.PackageIndex, error) {
	p.mutex.Lock()
	if opened, isOpened := p.opened[releaseVersion]; isOpened {
		p.mutex.Unlock()
		return opened, nil
	}
	p.mutex.Unlock()
	release, err := distro.Lookup(releaseVersion, distro.SupportedArch)
	if err != nil {
		return nil, err
	}
	options := p.options
	options.Release, options.Progress, options.Offline = release, progress, offline
	if offline {
		options.Refresh = false
	}
	opened, err := p.open(ctx, options)
	if err != nil {
		return nil, err
	}
	adapted := packageIndex{opened}
	p.mutex.Lock()
	p.opened[releaseVersion] = adapted
	p.mutex.Unlock()
	return adapted, nil
}

// packageIndex is an index.Index as the form sees it.
type packageIndex struct{ index *index.Index }

func (x packageIndex) Search(query, section string, limit int) ([]form.Match, int) {
	entries, total := x.index.Search(query, section, limit)
	matches := make([]form.Match, len(entries))
	for position, entry := range entries {
		matches[position] = form.Match(entry)
	}
	return matches, total
}

func (x packageIndex) SectionsMatching(query string) []form.SectionCount {
	counts := x.index.SectionsMatching(query)
	sections := make([]form.SectionCount, len(counts))
	for position, count := range counts {
		sections[position] = form.SectionCount(count)
	}
	return sections
}

func (x packageIndex) Lookup(name string) (form.Match, bool) {
	entry, isThere := x.index.Lookup(name)
	return form.Match(entry), isThere
}

func (x packageIndex) Has(name string) bool                    { return x.index.Has(name) }
func (x packageIndex) Nearest(name string, limit int) []string { return x.index.Nearest(name, limit) }
func (x packageIndex) Describe() string                        { return x.index.Describe() }
