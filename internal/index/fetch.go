package index

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ulikunitz/xz"

	"frostroot/internal/deb"
	"frostroot/internal/distro"
	"frostroot/internal/pki"
)

// responseHeaderTimeout bounds the wait for an archive to answer. There is
// no bound on the whole download: universe is 15 MB and links differ.
const responseHeaderTimeout = 30 * time.Second

// errChanged means a file did not match what Release said of it: the mirror
// published between the two requests, or the download was cut.
var errChanged = errors.New("does not match its Release entry")

// errTooLarge means a body kept coming past the size that bounds it: what
// its Release entry declared, or Options.MaxBodyBytes when nothing did.
// Nothing past that byte is read: the size is what bounds the download,
// and a server that declares one thing and sends another is refused at
// the bound rather than parsed to the end.
var errTooLarge = errors.New("longer than its declared size")

// boundedReader reads at most limit bytes and then reports errTooLarge
// rather than EOF, so a caller knows the body was cut short rather than
// finished.
type boundedReader struct {
	reader io.Reader
	limit  int64 // bytes still allowed
}

func (b *boundedReader) Read(buffer []byte) (int, error) {
	if b.limit <= 0 {
		return 0, errTooLarge
	}
	if int64(len(buffer)) > b.limit {
		buffer = buffer[:b.limit]
	}
	count, err := b.reader.Read(buffer)
	b.limit -= int64(count)
	return count, err
}

// listedFile is one line of a Release file's SHA256 section.
type listedFile struct {
	path   string // below dists/<pocket>/
	size   int64
	sha256 string
	// openSize is what a compressed index expands to: the size Release
	// lists for the same index uncompressed, or 0 when it lists none.
	openSize int64
}

// maxReleaseBytes bounds an InRelease or Release file, which nothing
// declares a size for: Ubuntu's are well under a megabyte, and a server
// that kept sending well-formed checksum lines would otherwise grow the
// list of files until the form ran out of memory.
const maxReleaseBytes = 16 << 20

// maxExpansion bounds what a compressed index may expand to when nothing
// declares its uncompressed size: this many times its compressed size.
// Package indexes compress about five to ten times; a body that expands
// further than this is not an index.
const maxExpansion = 64

// expandedLimit is maxExpansion times compressedBytes, at most ceiling.
func expandedLimit(compressedBytes, ceiling int64) int64 {
	if compressedBytes <= 0 || compressedBytes >= ceiling/maxExpansion {
		return ceiling
	}
	return maxExpansion * compressedBytes
}

// pocketPlan is what to download from one pocket.
type pocketPlan struct {
	pocket string
	files  []listedFile // one per component that the pocket has
}

// fetch downloads and reduces the pockets the target names: for a release,
// its own pocket and -updates, with -security left out because everything in
// it is copied to -updates and what it has first are kernels (the spike,
// 2026-09-18); for a source, the one suite it publishes.
func fetch(ctx context.Context, options Options, what target) ([]Entry, error) {
	client := options.Client
	if client == nil {
		transport := pki.Transport(options.RootCAs, options.Insecure)
		transport.ResponseHeaderTimeout = responseHeaderTimeout
		client = &http.Client{Transport: transport}
	}
	maxBodyBytes := options.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	fetcher := &fetcher{ctx: ctx, client: client, baseURL: what.baseURL, components: what.components, progress: options.Progress, maxBodyBytes: maxBodyBytes, stallTimeout: options.StallTimeout}
	if len(what.rpmRepositories) > 0 {
		return fetcher.fedora(what.rpmRepositories)
	}

	var plans []pocketPlan
	for position, pocket := range what.pockets {
		plan, err := fetcher.plan(pocket)
		if err != nil {
			// A frozen mirror has no -updates; an archive without the
			// release pocket is not an archive of this release.
			if position > 0 && errors.Is(err, errNotFound) {
				continue
			}
			return nil, err
		}
		plans = append(plans, plan)
		for _, file := range plan.files {
			fetcher.totalBytes += file.size
		}
	}

	byName := map[string]Entry{}
	for _, plan := range plans {
		entries, err := fetcher.pocket(plan)
		if errors.Is(err, errChanged) {
			// Once more, from the pocket's Release on: a mirror that was
			// mid-publication has finished by now, or is broken.
			fetcher.doneBytes -= fetcher.pocketBytes
			if plan, err = fetcher.plan(plan.pocket); err == nil {
				entries, err = fetcher.pocket(plan)
			}
		}
		if err != nil {
			return nil, err
		}
		// The newest version of each name, as apt would choose among them:
		// a vendor's repository lists every version of a package in one
		// file, in whatever order it likes. Between pockets of one version,
		// the later one's stanza stands.
		for _, entry := range entries {
			if kept, isKept := byName[entry.Name]; isKept && deb.CompareVersions(kept.Version, entry.Version) > 0 {
				continue
			}
			byName[entry.Name] = entry
		}
	}
	if len(byName) == 0 {
		return nil, fmt.Errorf("%s lists no packages for %s", fetcher.baseURL, what.suite)
	}
	entries := make([]Entry, 0, len(byName))
	for _, entry := range byName {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// errNotFound is a 404.
var errNotFound = errors.New("not found")

type fetcher struct {
	ctx         context.Context
	client      *http.Client
	baseURL     string
	components  []string
	progress    func(doneBytes, totalBytes int64)
	totalBytes  int64
	doneBytes   int64
	pocketBytes int64 // of doneBytes, what the pocket being fetched added
	// maxBodyBytes bounds what a file declares no size for: Fedora's
	// primary metadata when repomd.xml gives no open-size.
	maxBodyBytes int64
	stallTimeout time.Duration // Options.StallTimeout
}

// get opens url. The caller closes the body, which fails a read once it
// has sent nothing for the stall timeout.
func (f *fetcher) get(url string) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancelCause(f.ctx)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	response, err := f.client.Do(request)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	if response.StatusCode == http.StatusNotFound {
		_ = response.Body.Close()
		cancel(nil)
		return nil, fmt.Errorf("%s: %w", url, errNotFound)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		cancel(nil)
		return nil, fmt.Errorf("%s: %s", url, response.Status)
	}
	return guardStall(ctx, response.Body, cancel, f.stallTimeout), nil
}

// DefaultStallTimeout is how long a download of an index may receive
// nothing before it fails. The client's own timeout covers the headers
// only, and a server can send part of a body and then nothing, holding the
// field for as long as the person waits.
const DefaultStallTimeout = time.Minute

// errStalled means a download received nothing for the stall timeout.
var errStalled = errors.New("the download stalled")

// stalledBody is a response body whose request is canceled, with
// errStalled as the cause, once it has sent nothing for timeout.
type stalledBody struct {
	body    io.ReadCloser
	stalled func() bool // whether the request ended for a stall, not a cancel
	cancel  context.CancelCauseFunc
	timer   *time.Timer
	timeout time.Duration
}

// guardStall wraps body, whose request runs under ctx, which cancel ends.
func guardStall(ctx context.Context, body io.ReadCloser, cancel context.CancelCauseFunc, timeout time.Duration) io.ReadCloser {
	if timeout <= 0 {
		timeout = DefaultStallTimeout
	}
	return &stalledBody{
		body:    body,
		stalled: func() bool { return errors.Is(context.Cause(ctx), errStalled) },
		cancel:  cancel,
		timeout: timeout,
		timer:   time.AfterFunc(timeout, func() { cancel(errStalled) }),
	}
}

func (s *stalledBody) Read(buffer []byte) (int, error) {
	count, err := s.body.Read(buffer)
	if count > 0 {
		s.timer.Reset(s.timeout)
	}
	if err != nil && s.stalled() {
		err = fmt.Errorf("%w: nothing received for %s", errStalled, s.timeout)
	}
	return count, err
}

func (s *stalledBody) Close() error {
	s.timer.Stop()
	err := s.body.Close()
	s.cancel(nil)
	return err
}

// plan reads a pocket's Release file and picks, for every component, the
// smallest index it lists. InRelease is what apt reads and every current
// repository has; Release is the older spelling of the same content.
func (f *fetcher) plan(pocket string) (pocketPlan, error) {
	var listed map[string]listedFile
	var firstErr error
	for _, name := range []string{"InRelease", "Release"} {
		body, err := f.get(f.baseURL + "/dists/" + pocket + "/" + name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		listed, err = parseRelease(&boundedReader{reader: body, limit: maxReleaseBytes + 1})
		_ = body.Close()
		if errors.Is(err, errTooLarge) {
			return pocketPlan{}, fmt.Errorf("%s %s: more than %d bytes: %w", pocket, name, maxReleaseBytes, err)
		}
		if err != nil {
			return pocketPlan{}, fmt.Errorf("%s %s: %w", pocket, name, err)
		}
		break
	}
	if listed == nil {
		return pocketPlan{}, firstErr
	}
	plan := pocketPlan{pocket: pocket}
	for _, component := range f.components {
		uncompressed := component + "/binary-" + distro.SupportedArch + "/Packages"
		for _, extension := range []string{".xz", ".gz", ""} {
			if file, isListed := listed[uncompressed+extension]; isListed {
				file.openSize = listed[uncompressed].size
				plan.files = append(plan.files, file)
				break
			}
		}
	}
	return plan, nil
}

// parseRelease returns the files a Release file's SHA256 section lists, by
// path. The PGP armor of an InRelease file is not in that section and needs
// no special care.
func parseRelease(reader io.Reader) (map[string]listedFile, error) {
	listed := map[string]listedFile{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	inSection := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, " ") {
			inSection = line == "SHA256:"
			continue
		}
		if !inSection {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		size, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || size < 0 {
			continue
		}
		listed[fields[2]] = listedFile{path: fields[2], size: size, sha256: strings.ToLower(fields[0])}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(listed) == 0 {
		return nil, errors.New("no SHA256 section")
	}
	return listed, nil
}

// pocket downloads and reduces every file of plan.
func (f *fetcher) pocket(plan pocketPlan) ([]Entry, error) {
	f.pocketBytes = 0
	var entries []Entry
	for _, file := range plan.files {
		component, _, _ := strings.Cut(file.path, "/")
		fileEntries, err := f.file(plan.pocket, component, file)
		if err != nil {
			return nil, err
		}
		entries = append(entries, fileEntries...)
	}
	return entries, nil
}

// file streams one index: the body is hashed and counted as it is read,
// decompressed, and parsed paragraph by paragraph, so neither the 15 MB
// file nor its 73 MB of text is ever held. The entries count only once the
// size and the digest are what Release said.
//
// The size Release declared bounds the read, before decompression, so a
// server that keeps sending is cut off at that byte rather than parsed to
// the end: an index whose 1.7 MB expanded to 144 MB of stanzas once took
// 278 MB of heap before the size was ever compared, and a larger one would
// have ended the process, not the source.
//
// What it expands to is bounded too, as Fedora's metadata is: by the size
// Release lists for the uncompressed index, or maxExpansion times the
// compressed one when it lists none, and never past maxBodyBytes, since the
// same unsigned Release declares both. A small file that honestly matches
// its size and digest could otherwise expand without end.
func (f *fetcher) file(pocket, component string, file listedFile) ([]Entry, error) {
	url := f.baseURL + "/dists/" + pocket + "/" + file.path
	body, err := f.get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }() // the body has been read or the fetch failed
	hasher := sha256.New()
	// One byte past the declared size is enough to tell a longer body from
	// one of exactly that size, and is all a longer one gets to send.
	bounded := &boundedReader{reader: body, limit: file.size + 1}
	counted := &countingReader{reader: io.TeeReader(bounded, hasher), onRead: func(count int64) {
		f.doneBytes += count
		f.pocketBytes += count
		if f.progress != nil {
			f.progress(f.doneBytes, f.totalBytes)
		}
	}}
	text, err := decompress(file.path, bufio.NewReaderSize(counted, 256<<10))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	openLimit := file.openSize
	if openLimit <= 0 {
		openLimit = expandedLimit(file.size, f.maxBodyBytes)
	}
	opened := &boundedReader{reader: text, limit: min(openLimit, f.maxBodyBytes) + 1}
	var entries []Entry
	err = deb.ReadStanzas(opened, func(stanza deb.Stanza) error {
		if entry, isPackage := reduce(stanza, component); isPackage {
			entries = append(entries, entry)
		}
		return nil
	})
	if err != nil {
		if f.ctx.Err() != nil {
			return nil, f.ctx.Err()
		}
		if errors.Is(err, errTooLarge) || counted.count > file.size {
			return nil, fmt.Errorf("%s: %w", url, errTooLarge)
		}
		// A server that went quiet is not one that published anew, and
		// asking it again would only wait as long once more.
		if errors.Is(err, errStalled) {
			return nil, fmt.Errorf("%s: %w", url, err)
		}
		// A cut download fails in the decompressor before the digest is
		// ever compared.
		return nil, fmt.Errorf("%s: %w: %w", url, errChanged, err)
	}
	// A decompressor may stop before the last bytes of its input.
	if _, err := io.Copy(io.Discard, counted); err != nil && !errors.Is(err, errTooLarge) {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if counted.count > file.size {
		return nil, fmt.Errorf("%s: %w", url, errTooLarge)
	}
	if counted.count != file.size || hex.EncodeToString(hasher.Sum(nil)) != file.sha256 {
		return nil, fmt.Errorf("%s: %w", url, errChanged)
	}
	return entries, nil
}

// decompress returns the text of an index by its file name's extension.
func decompress(path string, reader io.Reader) (io.Reader, error) {
	switch {
	case strings.HasSuffix(path, ".xz"):
		return xz.NewReader(reader)
	case strings.HasSuffix(path, ".gz"):
		return gzip.NewReader(reader)
	default:
		return reader, nil
	}
}

// What an index keeps of one package is cut to these lengths. A real name
// is tens of bytes and a one-line description under a hundred; a stanza a
// server chose could make a field megabytes long, and the cache line that
// held it one longer than the cache reader takes, so that the cache was
// deleted and the repository fetched again on every run. A name past its
// limit names no real package and is not kept at all.
const (
	maxNameBytes        = 256
	maxFieldBytes       = 256
	maxDescriptionBytes = 512
)

// cutField returns text cut to at most limit bytes, at a rune boundary.
func cutField(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// reduce keeps what the picker shows of a Packages paragraph.
func reduce(stanza deb.Stanza, component string) (Entry, bool) {
	name := oneLine(stanza["Package"])
	if name == "" || len(name) > maxNameBytes {
		return Entry{}, false
	}
	section := stanza["Section"]
	if _, withoutComponent, hasComponent := strings.Cut(section, "/"); hasComponent {
		section = withoutComponent
	}
	description, _, _ := strings.Cut(stanza["Description"], "\n")
	// Every field goes through oneLine, not the description alone: the
	// cache is tab-separated, and a tab anywhere in a vendor's stanza would
	// write a line the next run cannot parse, which deletes the cache and
	// fetches that repository again for ever.
	return Entry{
		Name:        name,
		Version:     cutField(oneLine(stanza["Version"]), maxFieldBytes),
		Component:   component,
		Section:     cutField(oneLine(section), maxFieldBytes),
		Description: cutField(oneLine(description), maxDescriptionBytes),
	}, true
}

// oneLine makes text safe for a tab-separated line and for a terminal: a
// tab would split the line and a newline would end it, and deb.ReadStanzas
// joins a continuation line to its field with a newline, as a vendor's
// stanza may fold any field that way, not only the description.
func oneLine(text string) string {
	return strings.TrimSpace(strings.Map(lineSafeRune, text))
}

// lineSafeRune turns a tab, a carriage return or a line feed into a space
// and drops every other control character, C0, DEL and C1 alike. An index
// is read without verifying its signature, often over plain http, so these
// bytes are whoever is on the path's to choose; the picker draws them, and
// an escape sequence on a terminal is a command to it: moving the cursor,
// retitling the window, writing the clipboard. strings.Map has already
// turned a byte that is not UTF-8, a lone 0x9B among them, into U+FFFD.
func lineSafeRune(character rune) rune {
	switch {
	case character == '\t' || character == '\r' || character == '\n':
		return ' '
	case unicode.IsControl(character):
		return -1
	}
	return character
}

// countingReader reports every read.
type countingReader struct {
	reader io.Reader
	count  int64
	onRead func(count int64)
}

func (c *countingReader) Read(buffer []byte) (int, error) {
	count, err := c.reader.Read(buffer)
	c.count += int64(count)
	if count > 0 && c.onRead != nil {
		c.onRead(int64(count))
	}
	return count, err
}
