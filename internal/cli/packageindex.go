package cli

import (
	"context"
	"flag"
	"strings"
	"sync"

	"frostroot/internal/distro"
	"frostroot/internal/form"
	"frostroot/internal/index"
)

// indexFlags are what init, edit and capture take for the package index
// their form searches.
type indexFlags struct {
	mirror      string
	pythonIndex string
	caBundle    string
	refresh     bool
}

// addIndexFlags registers the index flags on a command's flag set.
func addIndexFlags(flags *flag.FlagSet) *indexFlags {
	parsed := &indexFlags{}
	flags.StringVar(&parsed.mirror, "mirror", "", "fetch the package index from this archive `URL` instead of Ubuntu's")
	flags.StringVar(&parsed.caBundle, "ca-bundle", "", "trust the certificate authorities in this PEM `FILE` when the mirror is https, as a network that inspects TLS needs")
	flags.StringVar(&parsed.pythonIndex, "python-index", "", "search this PEP 691 simple index `URL` instead of PyPI's")
	flags.BoolVar(&parsed.refresh, "refresh-index", false, "fetch the package index again even when the cached one is fresh")
	return parsed
}

// indexUsageText is what the usage of a command with the form says about
// the index; the flags themselves are listed after it.
const indexUsageText = `The form searches the release's apt archive and PyPI, each fetched once and
kept under $XDG_CACHE_HOME/frostroot/index (or ~/.cache) for a week. They
only suggest names: build checks every package against the signed archive,
and pip resolves the Python ones. --plain never fetches; it checks names
against the cached indexes when there are any.

`

// packageIndexes opens package indexes for one run of the recipe form: each
// release once, however often the form asks, because the picker opens it
// when it is reached and the summary asks again for its warning.
type packageIndexes struct {
	open func(ctx context.Context, options index.Options) (*index.Index, error)
	// openSource opens one third-party repository named by the answers, so
	// that the packages of a source the recipe adds are packages the picker
	// finds.
	openSource func(ctx context.Context, options index.Options, source index.Source) (*index.Index, error)
	options    index.Options // what every release shares: mirror, cache, trust
	// openPyPI and pypiOptions are the same for PyPI, which is one index
	// whatever release the image is. --mirror names an apt mirror, so it
	// must not be handed to PyPI as one.
	openPyPI    func(ctx context.Context, options index.Options) (*index.PyPIIndex, error)
	pypiOptions index.Options
	summaries   *index.Summaries

	mutex sync.Mutex
	// opened is the index a whole request was answered with, by its key;
	// archives and sources hold the parts it was made of, so that adding a
	// source does not fetch the archive again.
	opened   map[string]form.PackageIndex
	archives map[string]*index.Index
	sources  map[string]*index.Index
	pypi     form.PackageIndex
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
	a.ensureKeyClient(rootCAs)
	pypiOptions := options
	// --mirror is an apt mirror and says nothing about PyPI, which has its
	// own flag.
	pypiOptions.Mirror = parsed.pythonIndex
	return &packageIndexes{
		open:        index.Open,
		openSource:  index.OpenSource,
		options:     options,
		openPyPI:    index.OpenPyPI,
		pypiOptions: pypiOptions,
		summaries:   index.NewSummaries(a.IndexClient, options.RootCAs, pypiOptions.Mirror),
		opened:      map[string]form.PackageIndex{},
		archives:    map[string]*index.Index{},
		sources:     map[string]*index.Index{},
	}, true
}

// Open is the form.IndexOpener of this run.
func (p *packageIndexes) Open(ctx context.Context, request form.IndexRequest, progress func(doneBytes, totalBytes int64)) (form.PackageIndex, error) {
	return p.get(ctx, request, progress, false)
}

// Known returns the index a request asks for without ever fetching it: the
// one the form opened, or the cached one. nil when there is neither, and
// then nothing is known about any name.
func (p *packageIndexes) Known(request form.IndexRequest) form.PackageIndex {
	if p == nil {
		return nil
	}
	known, err := p.get(context.Background(), request, nil, true)
	if err != nil {
		return nil
	}
	return known
}

// get returns the release's archive merged with the sources the request
// names. A source whose repository cannot be read is left out and named in
// the line above the results: the archive alone is worth searching, and a
// vendor being down is no reason for the picker to stop working.
func (p *packageIndexes) get(ctx context.Context, request form.IndexRequest, progress func(int64, int64), offline bool) (form.PackageIndex, error) {
	key := request.Key()
	p.mutex.Lock()
	if opened, isOpened := p.opened[key]; isOpened {
		p.mutex.Unlock()
		return opened, nil
	}
	p.mutex.Unlock()
	release, err := distro.Lookup(request.Release, distro.SupportedArch)
	if err != nil {
		return nil, err
	}
	options := p.options
	options.Release, options.Offline = release, offline
	if offline {
		options.Refresh = false
	}
	fetched := &cumulativeProgress{report: progress}
	archive, err := p.archive(ctx, request.Release, options, fetched)
	if err != nil {
		return nil, err
	}
	parts := []*index.Index{archive}
	var unreachable []string
	for _, wanted := range request.Sources {
		opened, err := p.source(ctx, options, index.Source{
			Name:       wanted.Name,
			URL:        wanted.URL,
			Suite:      wanted.SuiteFor(release.Suite),
			Components: wanted.ComponentsOrDefault(),
		}, fetched)
		if err != nil {
			unreachable = append(unreachable, wanted.Name)
			continue
		}
		parts = append(parts, opened)
	}
	adapted := packageIndex{index.Union(parts, unreachable)}
	if len(unreachable) == 0 {
		// Only a whole answer is remembered. One missing a repository that
		// was down would otherwise be handed to every later question about
		// these answers, including after the network came back; the parts
		// it is made of are each cached, so building it again is cheap.
		p.mutex.Lock()
		p.opened[key] = adapted
		p.mutex.Unlock()
	}
	return adapted, nil
}

// archive opens a release's own index, once per release however many
// requests ask for it.
func (p *packageIndexes) archive(ctx context.Context, releaseVersion string, options index.Options, fetched *cumulativeProgress) (*index.Index, error) {
	p.mutex.Lock()
	opened, isOpened := p.archives[releaseVersion]
	p.mutex.Unlock()
	if isOpened {
		return opened, nil
	}
	options.Progress = fetched.of()
	opened, err := p.open(ctx, options)
	if err != nil {
		return nil, err
	}
	fetched.done()
	p.mutex.Lock()
	p.archives[releaseVersion] = opened
	p.mutex.Unlock()
	return opened, nil
}

// source opens one third-party repository, once per repository. A failure
// is not remembered: a vendor that was down when the picker was first
// reached may be up when the answers change and it is asked for again.
func (p *packageIndexes) source(ctx context.Context, options index.Options, wanted index.Source, fetched *cumulativeProgress) (*index.Index, error) {
	key := strings.Join(append([]string{wanted.Name, wanted.URL, wanted.Suite}, wanted.Components...), "\t")
	p.mutex.Lock()
	opened, isOpened := p.sources[key]
	p.mutex.Unlock()
	if isOpened {
		return opened, nil
	}
	options.Progress = fetched.of()
	opened, err := p.openSource(ctx, options, wanted)
	if err != nil {
		return nil, err
	}
	fetched.done()
	p.mutex.Lock()
	p.sources[key] = opened
	p.mutex.Unlock()
	return opened, nil
}

// cumulativeProgress adds several downloads up for one progress line. Each
// reports its own bytes from nought, so what came before is carried as a
// base: the line counts up, never back, and the total grows as each
// repository says how much it has. A repository whose index was already
// open reports nothing, which is the truth — nothing is being downloaded.
type cumulativeProgress struct {
	report func(doneBytes, totalBytes int64)
	mutex  sync.Mutex
	base   int64 // bytes the downloads before this one accounted for
	latest int64 // what this one has reported so far
}

// of returns the progress function of the next download, or nil when there
// is nobody to report to.
func (c *cumulativeProgress) of() func(doneBytes, totalBytes int64) {
	if c == nil || c.report == nil {
		return nil
	}
	return func(doneBytes, totalBytes int64) {
		c.mutex.Lock()
		c.latest = doneBytes
		base := c.base
		c.mutex.Unlock()
		c.report(base+doneBytes, base+totalBytes)
	}
}

// done folds the download that has finished into the base.
func (c *cumulativeProgress) done() {
	if c == nil || c.report == nil {
		return
	}
	c.mutex.Lock()
	c.base += c.latest
	c.latest = 0
	c.mutex.Unlock()
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

// OpenPython is the form.IndexOpener for the Python field. The release is
// ignored: PyPI is one index, whatever Ubuntu the image is built on.
func (p *packageIndexes) OpenPython(ctx context.Context, _ form.IndexRequest, progress func(doneBytes, totalBytes int64)) (form.PackageIndex, error) {
	return p.python(ctx, progress, false)
}

// KnownPython returns the PyPI index without ever fetching it: the one the
// form opened, or the cached one. nil when there is neither.
func (p *packageIndexes) KnownPython() form.PackageIndex {
	if p == nil {
		return nil
	}
	known, err := p.python(context.Background(), nil, true)
	if err != nil {
		return nil
	}
	return known
}

func (p *packageIndexes) python(ctx context.Context, progress func(int64, int64), offline bool) (form.PackageIndex, error) {
	p.mutex.Lock()
	if p.pypi != nil {
		opened := p.pypi
		p.mutex.Unlock()
		return opened, nil
	}
	p.mutex.Unlock()
	options := p.pypiOptions
	options.Progress, options.Offline = progress, offline
	if offline {
		options.Refresh = false
	}
	opened, err := p.openPyPI(ctx, options)
	if err != nil {
		return nil, err
	}
	adapted := pypiIndex{index: opened, summaries: p.summaries}
	p.mutex.Lock()
	p.pypi = adapted
	p.mutex.Unlock()
	return adapted, nil
}

// pypiIndex is an index.PyPIIndex as the form sees it. PyPI publishes no
// version, section or description, so a match carries its name alone and
// the summary is looked up one at a time.
type pypiIndex struct {
	index     *index.PyPIIndex
	summaries *index.Summaries
}

func (x pypiIndex) Search(query, section string, limit int) ([]form.Match, int) {
	projects, total := x.index.Search(query, section, limit)
	matches := make([]form.Match, len(projects))
	for position, project := range projects {
		matches[position] = form.Match{Name: project.Name}
	}
	return matches, total
}

// SectionsMatching returns none: PyPI has no sections to narrow to.
func (x pypiIndex) SectionsMatching(string) []form.SectionCount { return nil }

func (x pypiIndex) Lookup(name string) (form.Match, bool) {
	project, isThere := x.index.Lookup(name)
	return form.Match{Name: project.Name}, isThere
}

func (x pypiIndex) Has(name string) bool                    { return x.index.Has(name) }
func (x pypiIndex) Nearest(name string, limit int) []string { return x.index.Nearest(name, limit) }
func (x pypiIndex) Describe() string                        { return x.index.Describe() }

// Summary implements form.PackageSummaries.
func (x pypiIndex) Summary(name string) (string, bool) { return x.summaries.Cached(name) }

// FetchSummary implements form.PackageSummaries.
func (x pypiIndex) FetchSummary(ctx context.Context, name string) { x.summaries.Fetch(ctx, name) }
