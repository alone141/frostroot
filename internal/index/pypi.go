package index

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"frostroot/internal/pki"
	"frostroot/internal/recipe"
)

// PyPIIndexURL is the index PyPI publishes every project name at.
const PyPIIndexURL = "https://pypi.org/simple/"

// pypiSimpleJSON is the PEP 691 form of the simple index: the same names,
// as JSON rather than a page of links.
const pypiSimpleJSON = "application/vnd.pypi.simple.v1+json"

// pypiCacheFormat heads a cached PyPI index, as cacheFormat heads an apt one.
const pypiCacheFormat = "frostroot-pypi 1"

// Project is one PyPI project. PyPI publishes no version, section or
// description with the index — only the name — so this is all there is until
// something asks Summaries for one.
type Project struct {
	Name string // as PyPI spells it: Flask-SQLAlchemy
	// normalized is Name under PEP 503, which is how PyPI compares names:
	// Flask_SQLAlchemy, flask-sqlalchemy and Flask.SQLAlchemy are one
	// project, and a recipe may spell it any of those ways.
	normalized string
}

// PyPIIndex is every project name PyPI publishes, sorted by the normalized
// name.
type PyPIIndex struct {
	projects     []Project
	fetched      time.Time
	staleBecause error
	now          func() time.Time
}

// OpenPyPI returns PyPI's project names: from the cache when it is fresh,
// from PyPI otherwise, and from a stale cache when PyPI cannot be reached.
// An error wraps ErrUnavailable unless ctx ended.
func OpenPyPI(ctx context.Context, options Options) (*PyPIIndex, error) {
	if options.MaxAge == 0 {
		options.MaxAge = DefaultMaxAge
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	cached := readPyPICache(options)
	if cached != nil {
		fresh := options.Now().Sub(cached.fetched) < options.MaxAge
		if options.Offline || (fresh && !options.Refresh) {
			return cached, nil
		}
	}
	if options.Offline {
		return nil, fmt.Errorf("%w: nothing cached for PyPI, and this command does not fetch", ErrUnavailable)
	}
	projects, err := fetchPyPI(ctx, options)
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
	fetched := newPyPIIndex(options.Now(), projects, options.Now)
	_ = writePyPICache(options, fetched)
	return fetched, nil
}

// newPyPIIndex sorts projects by their normalized name and returns the
// index. A name the cache could not give back is left out: the cache is a
// line per name, read back in strictly increasing normalized order, so a
// name that is not a PEP 503 name, or a second spelling of a name already
// there, would make the file unreadable and the whole index fetch again on
// every run. pypi.org publishes neither; a private index may, and no recipe
// could name such a project anyway.
func newPyPIIndex(fetched time.Time, projects []Project, now func() time.Time) *PyPIIndex {
	kept := make([]Project, 0, len(projects))
	for _, project := range projects {
		if recipe.CheckPythonPackageName(project.Name) == nil {
			kept = append(kept, project)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].normalized < kept[j].normalized })
	kept = slices.CompactFunc(kept, func(a, b Project) bool { return a.normalized == b.normalized })
	return &PyPIIndex{projects: kept, fetched: fetched, now: now}
}

// Len returns how many projects the index holds.
func (x *PyPIIndex) Len() int { return len(x.projects) }

// Describe says what the index is, for the line above the results.
func (x *PyPIIndex) Describe() string {
	description := fmt.Sprintf("PyPI · %s projects · fetched %s", groupThousands(len(x.projects)), ago(x.now().Sub(x.fetched)))
	if x.staleBecause != nil {
		description += " · PyPI not reachable"
	}
	return description
}

// Lookup returns the project of exactly this name, comparing as PEP 503
// does, so that Flask_SQLAlchemy finds Flask-SQLAlchemy.
func (x *PyPIIndex) Lookup(name string) (Project, bool) {
	normalized := recipe.NormalizePythonName(name)
	position := sort.Search(len(x.projects), func(i int) bool { return x.projects[i].normalized >= normalized })
	if position < len(x.projects) && x.projects[position].normalized == normalized {
		return x.projects[position], true
	}
	return Project{}, false
}

// Has reports whether PyPI has a project of this name.
func (x *PyPIIndex) Has(name string) bool {
	_, isThere := x.Lookup(name)
	return isThere
}

// Search returns the projects matching query, best first, at most limit of
// them, and how many matched in all. Rank: the exact name, names that start
// with the query, then names that contain it; shorter names first inside a
// rank. There is no description to search and no section to filter by, so
// section is ignored.
func (x *PyPIIndex) Search(query, _ string, limit int) (matches []Project, total int) {
	query = recipe.NormalizePythonName(strings.TrimSpace(query))
	var ranked [rankDescription][]int
	for position, project := range x.projects {
		rank, isMatch := rankOfName(project.normalized, query)
		if !isMatch {
			continue
		}
		ranked[rank] = append(ranked[rank], position)
		total++
	}
	for rank, positions := range ranked {
		if len(matches) >= limit {
			break
		}
		if query != "" && rank != rankExact {
			sort.SliceStable(positions, func(i, j int) bool {
				return len(x.projects[positions[i]].Name) < len(x.projects[positions[j]].Name)
			})
		}
		for _, position := range positions {
			if len(matches) >= limit {
				break
			}
			matches = append(matches, x.projects[position])
		}
	}
	return matches, total
}

// rankOfName ranks a normalized name against a normalized query.
func rankOfName(name, query string) (int, bool) {
	switch {
	case query == "":
		return rankName, true
	case name == query:
		return rankExact, true
	case strings.HasPrefix(name, query):
		return rankPrefix, true
	case strings.Contains(name, query):
		return rankName, true
	}
	return 0, false
}

// Nearest returns up to limit names close to one PyPI does not have, the
// closest first.
func (x *PyPIIndex) Nearest(name string, limit int) []string {
	normalized := recipe.NormalizePythonName(name)
	bound := 2
	if len(normalized) > 12 {
		bound = 3
	}
	type candidate struct {
		name     string
		distance int
	}
	var candidates []candidate
	rows := [2][]int{make([]int, len(normalized)+bound+1), make([]int, len(normalized)+bound+1)}
	for _, project := range x.projects {
		if difference := len(project.normalized) - len(normalized); difference > bound || -difference > bound {
			continue
		}
		if distance := editDistance(normalized, project.normalized, bound, rows); distance <= bound {
			candidates = append(candidates, candidate{name: project.Name, distance: distance})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].distance < candidates[j].distance })
	var nearest []string
	for _, found := range candidates {
		if len(nearest) >= limit {
			break
		}
		nearest = append(nearest, found.name)
	}
	return nearest
}

// pypiIndexURL is where the names come from: PyPI, or a mirror.
func pypiIndexURL(options Options) string {
	if options.Mirror != "" {
		return strings.TrimRight(options.Mirror, "/") + "/"
	}
	return PyPIIndexURL
}

// fetchPyPI downloads and reduces the simple index. The body is asked for
// gzipped and decompressed here rather than by the transport, so that the
// progress the user sees counts the bytes actually on the wire against the
// Content-Length that describes them.
func fetchPyPI(ctx context.Context, options Options) ([]Project, error) {
	client := options.Client
	if client == nil {
		transport := pki.Transport(options.RootCAs, options.Insecure)
		transport.ResponseHeaderTimeout = responseHeaderTimeout
		// The transport must not add Accept-Encoding itself, or it would
		// decompress transparently and the count below would be of the
		// decompressed stream.
		transport.DisableCompression = true
		client = &http.Client{Transport: transport}
	}
	url := pypiIndexURL(options)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", pypiSimpleJSON)
	request.Header.Set("Accept-Encoding", "gzip")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }() // read, or the fetch failed
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, response.Status)
	}
	total := response.ContentLength
	// A Content-Length bounds the body already: the client stops at it.
	// A body sent without one, chunked, would be read to whatever end the
	// server chose, so it gets a ceiling nothing legitimate reaches, read
	// before decompression. One byte past the ceiling tells a longer body
	// from one of exactly that size.
	ceiling := options.MaxBodyBytes
	if ceiling <= 0 {
		ceiling = DefaultMaxBodyBytes
	}
	if total >= 0 {
		ceiling = total
	}
	bounded := &boundedReader{reader: response.Body, limit: ceiling + 1}
	counted := &countingReader{reader: bounded, onRead: func(count int64) {
		if options.Progress != nil {
			options.Progress(count, total)
		}
	}}
	var body io.Reader = bufio.NewReaderSize(counted, 256<<10)
	if strings.EqualFold(response.Header.Get("Content-Encoding"), "gzip") {
		unzipped, err := gzip.NewReader(body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", url, err)
		}
		body = unzipped
	}
	projects, err := readSimpleIndex(body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, errTooLarge) || counted.count > ceiling {
			return nil, fmt.Errorf("%s: %w", url, errTooLarge)
		}
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	// The decoder stops at the document's closing brace; whatever a server
	// sends after it is read up to the ceiling, and one byte past it is the
	// whole of what a longer body gets to say.
	if _, err := io.Copy(io.Discard, counted); err != nil && !errors.Is(err, errTooLarge) {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if counted.count > ceiling {
		return nil, fmt.Errorf("%s: %w", url, errTooLarge)
	}
	if len(projects) == 0 {
		return nil, fmt.Errorf("%s lists no projects", url)
	}
	return projects, nil
}

// readSimpleIndex streams the projects out of a PEP 691 document, so that
// its forty megabytes are never held as one value.
func readSimpleIndex(reader io.Reader) ([]Project, error) {
	decoder := json.NewDecoder(reader)
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, isDelimiter := token.(json.Delim); !isDelimiter || delimiter != '{' {
		return nil, errors.New("not a simple index: expected an object")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		field, isString := token.(string)
		if !isString {
			return nil, errors.New("not a simple index: expected a field name")
		}
		if field != "projects" {
			var ignored json.RawMessage
			if err := decoder.Decode(&ignored); err != nil {
				return nil, err
			}
			continue
		}
		return readProjects(decoder)
	}
	return nil, errors.New("not a simple index: no projects")
}

// readProjects reads the array of the projects field.
func readProjects(decoder *json.Decoder) ([]Project, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, isDelimiter := token.(json.Delim); !isDelimiter || delimiter != '[' {
		return nil, errors.New("not a simple index: projects is not a list")
	}
	projects := make([]Project, 0, 1<<20)
	for decoder.More() {
		var project struct {
			Name string `json:"name"`
		}
		if err := decoder.Decode(&project); err != nil {
			return nil, err
		}
		if project.Name == "" {
			continue
		}
		projects = append(projects, Project{Name: project.Name, normalized: recipe.NormalizePythonName(project.Name)})
	}
	return projects, nil
}

// pypiCachePath is the file the PyPI names are kept in.
func pypiCachePath(options Options) string {
	if options.CacheDir == "" {
		return ""
	}
	return filepath.Join(options.CacheDir, "pypi.tsv.gz")
}

// readPyPICache returns the cached index, or nil: no cache, a cache of
// another index URL, or one that does not parse, which is deleted.
func readPyPICache(options Options) *PyPIIndex {
	path := pypiCachePath(options)
	if path == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }() // read only; nothing is lost
	url, fetched, projects, err := parsePyPICache(file)
	if err != nil {
		_ = file.Close() // so that the file can go, where an open one cannot
		removeQuietly(path)
		return nil
	}
	if url != pypiIndexURL(options) {
		return nil
	}
	return newPyPIIndex(fetched, projects, options.Now)
}

// parsePyPICache reads a whole cache file: a header line, then one name a
// line, in order.
func parsePyPICache(file *os.File) (url string, fetched time.Time, projects []Project, err error) {
	text, err := gzip.NewReader(bufio.NewReaderSize(file, 256<<10))
	if err != nil {
		return "", time.Time{}, nil, err
	}
	scanner := bufio.NewScanner(text)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	if !scanner.Scan() {
		return "", time.Time{}, nil, errors.New("empty index file")
	}
	fields := strings.Split(scanner.Text(), "\t")
	if len(fields) != 3 || fields[0] != pypiCacheFormat {
		return "", time.Time{}, nil, errors.New("not a frostroot PyPI index of this format")
	}
	fetched, err = time.Parse(time.RFC3339, fields[2])
	if err != nil {
		return "", time.Time{}, nil, fmt.Errorf("unreadable fetch time: %w", err)
	}
	previous := ""
	for scanner.Scan() {
		name := scanner.Text()
		if name == "" {
			return "", time.Time{}, nil, errors.New("empty name")
		}
		normalized := recipe.NormalizePythonName(name)
		if normalized <= previous {
			return "", time.Time{}, nil, fmt.Errorf("unsorted index after %q", previous)
		}
		previous = normalized
		projects = append(projects, Project{Name: name, normalized: normalized})
	}
	if err := scanner.Err(); err != nil {
		return "", time.Time{}, nil, err
	}
	if len(projects) == 0 {
		return "", time.Time{}, nil, errors.New("index file without projects")
	}
	return fields[1], fetched, projects, nil
}

// writePyPICache stores the index for the next run, by rename.
func writePyPICache(options Options, built *PyPIIndex) (err error) {
	path := pypiCachePath(options)
	if path == "" {
		return errors.New("no cache directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = temporary.Close()
			removeQuietly(temporary.Name())
		}
	}()
	compressed := gzip.NewWriter(temporary)
	buffered := bufio.NewWriterSize(compressed, 256<<10)
	header := strings.Join([]string{pypiCacheFormat, pypiIndexURL(options), built.fetched.UTC().Format(time.RFC3339)}, "\t")
	if _, err = buffered.WriteString(header + "\n"); err != nil {
		return err
	}
	for _, project := range built.projects {
		if _, err = buffered.WriteString(project.Name + "\n"); err != nil {
			return err
		}
	}
	if err = buffered.Flush(); err != nil {
		return err
	}
	if err = compressed.Close(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
