package cli

import (
	"context"
	"flag"
	"strings"
	"sync"
	"time"

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
	insecure    bool
	refresh     bool
}

// addIndexFlags registers the index flags on a command's flag set.
func addIndexFlags(flags *flag.FlagSet) *indexFlags {
	parsed := &indexFlags{}
	flags.StringVar(&parsed.mirror, "mirror", "", "fetch the package index from this archive `URL` instead of Ubuntu's")
	flags.StringVar(&parsed.caBundle, "ca-bundle", "", "trust the certificate authorities in this PEM `FILE` when fetching an https mirror, source or key, as a network that inspects TLS needs")
	flags.BoolVar(&parsed.insecure, "insecure", false, insecureFlagUsage)
	flags.StringVar(&parsed.pythonIndex, "python-index", "", "search this PEP 691 simple index `URL` instead of PyPI's")
	flags.BoolVar(&parsed.refresh, "refresh-index", false, "fetch the package index again even when the cached one is fresh")
	return parsed
}

// indexUsageText is what the usage of a command with the form says about
// the index; the flags themselves are listed after it.
const indexUsageText = `The form searches the release's apt archive, the sources the recipe adds
and PyPI, each fetched once and kept under $XDG_CACHE_HOME/frostroot/index
(or ~/.cache) for a week. They
only suggest names: build checks every package against the signed archive,
and pip resolves the Python ones. --plain never fetches; it checks names
against the cached indexes when there are any.

--insecure skips certificate verification for those fetches and for the
signing keys of the sources you pick. The names are only suggestions, but a
PPA's key is then fetched from wherever the network says, and every later
build trusts it; the command says where to check it.

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
	// insecure is --insecure, which the key fetch after the form needs to
	// know as well as the indexes: a key found over an unverified connection
	// deserves a word.
	insecure bool

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
	options := index.Options{Mirror: parsed.mirror, CacheDir: index.CacheDir(a.Getenv), Refresh: parsed.refresh, Client: a.IndexClient, Insecure: parsed.insecure}
	rootCAs, ok := a.trustPool(parsed.caBundle)
	if !ok {
		return nil, false
	}
	options.RootCAs = rootCAs
	a.ensureKeyClient(rootCAs, parsed.insecure)
	if parsed.insecure {
		a.warnInsecure(insecureFormDetail)
	}
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
		summaries:   index.NewSummaries(pypiOptions),
		insecure:    parsed.insecure,
		opened:      map[string]form.PackageIndex{},
		archives:    map[string]*index.Index{},
		sources:     map[string]*index.Index{},
	}, true
}

// insecureTLS reports whether this run skips certificate verification. nil
// is a run with no indexes at all, which the tests make, and it verifies.
func (p *packageIndexes) insecureTLS() bool {
	return p != nil && p.insecure
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
	// An offline answer is remembered under a key of its own. It is built
	// from whatever is cached, so letting it share a key with a fetch would
	// let a week-old cache read on the summary page displace the index
	// --refresh-index had just gone and got.
	key := request.Key()
	if offline {
		key += "\x00offline"
	}
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
	var parts []*index.Index
	var unreachable []string
	// The archive not being there is no more fatal than a source not being
	// there: a recipe that adds Docker can still be told what Docker has.
	// Only when nothing at all can be read is there no index, and then the
	// archive's own error is the one worth reporting.
	archive, archiveErr := p.archive(ctx, request.Release, options, fetched, offline)
	if archiveErr == nil {
		parts = append(parts, archive)
	} else {
		unreachable = append(unreachable, "archive")
	}
	for _, wanted := range request.Sources {
		opened, err := p.source(ctx, options, index.Source{
			Name:       wanted.Name,
			URL:        wanted.URL,
			Suite:      wanted.SuiteFor(release.Suite),
			Components: wanted.ComponentsOrDefault(),
		}, fetched, offline)
		if err != nil {
			unreachable = append(unreachable, wanted.Name)
			continue
		}
		parts = append(parts, opened)
	}
	merged := index.Union(parts, unreachable)
	if merged == nil {
		return nil, archiveErr
	}
	adapted := packageIndex{merged}
	// Only a whole answer is remembered, so that a repository which was
	// down is tried again rather than left out of every later answer. What
	// is missing is what the merged index says, not the failures counted
	// above: a repository that could not be reached but has a cache comes
	// back as that cache, without an error, marked missing. An offline
	// answer is remembered whatever it is missing: it fetches nothing, so
	// asking it again would miss exactly the same thing at the price of
	// merging the whole archive afresh.
	if _, missing := merged.Sources(); len(missing) == 0 || offline {
		p.mutex.Lock()
		p.opened[key] = adapted
		p.mutex.Unlock()
	}
	return adapted, nil
}

// archive opens a release's own index, once per release however many
// requests ask for it.
func (p *packageIndexes) archive(ctx context.Context, releaseVersion string, options index.Options, fetched *cumulativeProgress, offline bool) (*index.Index, error) {
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
	if !offline && isWhole(opened) {
		// What a fetch opened is what later questions get. An offline read
		// keeps to its own answer: it may be a stale cache, and the fetch it
		// would displace is the one that was asked for. A cache served
		// because the archive could not be reached is not kept either, so
		// that the archive is tried again.
		p.mutex.Lock()
		p.archives[releaseVersion] = opened
		p.mutex.Unlock()
	}
	return opened, nil
}

// isWhole reports whether an index was read from its repository rather than
// served from a cache because the repository could not be reached, which
// index.Open reports as the repository missing rather than as an error.
func isWhole(opened *index.Index) bool {
	_, missing := opened.Sources()
	return len(missing) == 0
}

// source opens one third-party repository, once per repository. A failure
// is not remembered: a vendor that was down when the picker was first
// reached may be up when the answers change and it is asked for again.
func (p *packageIndexes) source(ctx context.Context, options index.Options, wanted index.Source, fetched *cumulativeProgress, offline bool) (*index.Index, error) {
	key := strings.Join(append([]string{wanted.Name, wanted.URL, wanted.Suite}, wanted.Components...), "\t")
	p.mutex.Lock()
	opened, isOpened := p.sources[key]
	p.mutex.Unlock()
	if isOpened {
		return opened, nil
	}
	options.Progress = fetched.of()
	if !offline {
		// One repository may not hold up the rest. A host that answers
		// nothing at all would otherwise cost the picker two waits of the
		// header timeout, once for InRelease and once for Release, before
		// the next source is even tried.
		withDeadline, stop := context.WithTimeout(ctx, sourceTimeout)
		defer stop()
		ctx = withDeadline
	}
	opened, err := p.openSource(ctx, options, wanted)
	if err != nil {
		fetched.abandon()
		return nil, err
	}
	fetched.done()
	if !offline && isWhole(opened) {
		p.mutex.Lock()
		p.sources[key] = opened
		p.mutex.Unlock()
	}
	return opened, nil
}

// sourceTimeout bounds one source's whole fetch. A source is a suite's
// InRelease and one Packages file, tens to hundreds of kilobytes; the
// archive, which is 21 MB, has no such bound and needs none, because it is
// the thing being waited for rather than something in the way of it.
const sourceTimeout = 20 * time.Second

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
// is nobody to report to. It clears what the last one reported, so a
// download that never reports — one answered from the cache — adds nothing,
// and one that failed leaves nothing.
func (c *cumulativeProgress) of() func(doneBytes, totalBytes int64) {
	if c == nil || c.report == nil {
		return nil
	}
	c.mutex.Lock()
	c.latest = 0
	c.mutex.Unlock()
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

// abandon forgets a download that failed. Its bytes bought nothing, and
// folding them in would leave the line counting past a total no repository
// is going to deliver.
func (c *cumulativeProgress) abandon() {
	if c == nil || c.report == nil {
		return
	}
	c.mutex.Lock()
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

// Sources implements form.PackageRepositories: which repositories the index
// holds, and which it could not read.
func (x packageIndex) Sources() ([]string, []string) { return x.index.Sources() }

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
