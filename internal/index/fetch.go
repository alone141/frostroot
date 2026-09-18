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

// listedFile is one line of a Release file's SHA256 section.
type listedFile struct {
	path   string // below dists/<pocket>/
	size   int64
	sha256 string
}

// pocketPlan is what to download from one pocket.
type pocketPlan struct {
	pocket string
	files  []listedFile // one per component that the pocket has
}

// fetch downloads and reduces the release pocket and -updates. -security is
// left out: everything in it is copied to -updates, and what it has first
// are kernels (the spike, 2026-09-18).
func fetch(ctx context.Context, options Options) ([]Entry, error) {
	client := options.Client
	if client == nil {
		transport := pki.Transport(options.RootCAs)
		transport.ResponseHeaderTimeout = responseHeaderTimeout
		client = &http.Client{Transport: transport}
	}
	baseURL := options.Release.ArchiveURL
	if options.Mirror != "" {
		baseURL = options.Mirror
	}
	fetcher := &fetcher{ctx: ctx, client: client, baseURL: strings.TrimRight(baseURL, "/"), components: options.Release.Components, progress: options.Progress}

	pockets := []string{options.Release.Suite, options.Release.Suite + "-updates"}
	var plans []pocketPlan
	for position, pocket := range pockets {
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
		for _, entry := range entries {
			byName[entry.Name] = entry // the later pocket's version wins
		}
	}
	if len(byName) == 0 {
		return nil, fmt.Errorf("%s lists no packages for %s", fetcher.baseURL, options.Release.Suite)
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
}

// get opens url. The caller closes the body.
func (f *fetcher) get(url string) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(f.ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := f.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusNotFound {
		_ = response.Body.Close()
		return nil, fmt.Errorf("%s: %w", url, errNotFound)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, response.Status)
	}
	return response.Body, nil
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
		listed, err = parseRelease(body)
		_ = body.Close()
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
		for _, extension := range []string{".xz", ".gz", ""} {
			if file, isListed := listed[component+"/binary-"+distro.SupportedArch+"/Packages"+extension]; isListed {
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
func (f *fetcher) file(pocket, component string, file listedFile) ([]Entry, error) {
	url := f.baseURL + "/dists/" + pocket + "/" + file.path
	body, err := f.get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }() // the body has been read or the fetch failed
	hasher := sha256.New()
	counted := &countingReader{reader: io.TeeReader(body, hasher), onRead: func(count int64) {
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
	var entries []Entry
	err = deb.ReadStanzas(text, func(stanza deb.Stanza) error {
		if entry, isPackage := reduce(stanza, component); isPackage {
			entries = append(entries, entry)
		}
		return nil
	})
	if err != nil {
		if f.ctx.Err() != nil {
			return nil, f.ctx.Err()
		}
		// A cut download fails in the decompressor before the digest is
		// ever compared.
		return nil, fmt.Errorf("%s: %w: %w", url, errChanged, err)
	}
	// A decompressor may stop before the last bytes of its input.
	if _, err := io.Copy(io.Discard, counted); err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
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

// reduce keeps what the picker shows of a Packages paragraph.
func reduce(stanza deb.Stanza, component string) (Entry, bool) {
	name := stanza["Package"]
	if name == "" {
		return Entry{}, false
	}
	section := stanza["Section"]
	if _, withoutComponent, hasComponent := strings.Cut(section, "/"); hasComponent {
		section = withoutComponent
	}
	description, _, _ := strings.Cut(stanza["Description"], "\n")
	return Entry{
		Name:        name,
		Version:     stanza["Version"],
		Component:   component,
		Section:     section,
		Description: oneLine(description),
	}, true
}

// oneLine makes text safe for a tab-separated line.
func oneLine(text string) string {
	return strings.TrimSpace(strings.NewReplacer("\t", " ", "\r", " ").Replace(text))
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
