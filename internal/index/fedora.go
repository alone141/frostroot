package index

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"frostroot/internal/distro"
)

// OpenFedora returns the index of a Fedora release: the packages its
// repositories offer for this architecture, the newest of each name, with
// the updates repository's in place of the release's own. It is cached and
// fetched by the apt index's rules: a week, Refresh, and nothing fetched
// Offline. options.Release and options.Mirror name an Ubuntu archive and
// are not read.
//
// Like the apt index it only suggests names. repomd.xml is read unsigned,
// and every file it names is checked against the size and digest it gives,
// which guards against a download cut short or a repository republished
// between two requests; build installs from the repositories with the
// release's own key and never reads this index.
func OpenFedora(ctx context.Context, options Options, release distro.FedoraRelease) (*Index, error) {
	return openTarget(ctx, options, fedoraTarget(release))
}

// fedoraTarget is a Fedora release's repositories, indexed as one, as a
// release's pockets are for Ubuntu. The cache header names every base URL
// and repository, so a table that changes either fetches again.
func fedoraTarget(release distro.FedoraRelease) target {
	baseURLs := make([]string, 0, len(release.Repositories))
	ids := make([]string, 0, len(release.Repositories))
	for _, repository := range release.Repositories {
		baseURLs = append(baseURLs, strings.TrimRight(repository.BaseURL, "/"))
		ids = append(ids, repository.ID)
	}
	return target{
		label:           "Fedora " + release.Version,
		cacheName:       "fedora-" + release.Version,
		baseURL:         strings.Join(baseURLs, " "),
		suite:           "fedora-" + release.Version,
		components:      ids,
		maxAge:          DefaultMaxAge,
		rpmRepositories: release.Repositories,
	}
}

// maxRepomdBytes bounds repomd.xml, which declares no size of its own.
// Fedora's are 6 and 7 kB.
const maxRepomdBytes = 1 << 20

// maxZstdWindow bounds the memory one zstd frame of primary.xml may ask
// for. Fedora 44's ask for 4 MiB; the decoder's own default of 512 MB is
// more than a suggestion list should let a server claim.
const maxZstdWindow = 64 << 20

// repomdFile is a file repomd.xml lists.
type repomdFile struct {
	href     string // below the repository's base URL
	size     int64  // as downloaded
	openSize int64  // decompressed; 0 when repomd.xml declares none
	sha256   string // of the file as downloaded
}

// rpmPlan is what to download from one repository.
type rpmPlan struct {
	repository distro.FedoraRepository
	baseURL    string
	primary    repomdFile
}

// fedora downloads and reduces the primary metadata of every repository,
// the later repository's packages in place of the earlier's, as the apt
// index takes -updates over the release pocket.
func (f *fetcher) fedora(repositories []distro.FedoraRepository) ([]Entry, error) {
	plans := make([]rpmPlan, 0, len(repositories))
	for _, repository := range repositories {
		plan, err := f.rpmPlan(repository)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
		f.totalBytes += plan.primary.size
	}
	byName := map[string]Entry{}
	for _, plan := range plans {
		entries, err := f.primary(plan)
		if errors.Is(err, errChanged) || errors.Is(err, errNotFound) {
			// Once more, from repomd.xml on: a repository republished
			// between the two requests names a new primary file, and the
			// one it named before is replaced or gone.
			f.doneBytes -= f.pocketBytes
			var replanned rpmPlan
			if replanned, err = f.rpmPlan(plan.repository); err == nil {
				f.totalBytes += replanned.primary.size - plan.primary.size
				entries, err = f.primary(replanned)
			}
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			byName[entry.Name] = entry // the later repository's wins
		}
	}
	if len(byName) == 0 {
		return nil, fmt.Errorf("the release's repositories list no packages for %s", distro.FedoraArch)
	}
	entries := make([]Entry, 0, len(byName))
	for _, entry := range byName {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// rpmPlan reads a repository's repomd.xml for where its primary metadata
// is, how large it is, and its digest.
func (f *fetcher) rpmPlan(repository distro.FedoraRepository) (rpmPlan, error) {
	baseURL := strings.TrimRight(repository.BaseURL, "/")
	url := baseURL + "/repodata/repomd.xml"
	body, err := f.get(url)
	if err != nil {
		return rpmPlan{}, err
	}
	defer func() { _ = body.Close() }() // read to its end or abandoned; nothing is written
	primary, err := parseRepomd(&boundedReader{reader: body, limit: maxRepomdBytes})
	if err != nil {
		return rpmPlan{}, fmt.Errorf("%s: %w", url, err)
	}
	return rpmPlan{repository: repository, baseURL: baseURL, primary: primary}, nil
}

// repomdDocument is the part of repomd.xml the index reads. The tags name
// no namespace, so they match repomd's own.
type repomdDocument struct {
	Data []struct {
		Type     string `xml:"type,attr"`
		Checksum struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"checksum"`
		Location struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
		Size     int64 `xml:"size"`
		OpenSize int64 `xml:"open-size"`
	} `xml:"data"`
}

// repodataHref is a relative path of plain names below the base URL, such
// as repodata/c48e…-primary.xml.zst: no scheme, no host, no leading slash.
var repodataHref = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9_][A-Za-z0-9._-]*)*$`)

// sha256Hex is a SHA-256 digest as repomd.xml writes it.
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// parseRepomd returns the primary metadata a repomd.xml lists. Everything
// it says is checked before anything is fetched on its word: the file must
// be below the repository, with a SHA-256 digest and a size.
func parseRepomd(reader io.Reader) (repomdFile, error) {
	var document repomdDocument
	if err := xml.NewDecoder(reader).Decode(&document); err != nil {
		if errors.Is(err, errTooLarge) {
			return repomdFile{}, errTooLarge
		}
		return repomdFile{}, fmt.Errorf("unreadable: %w", err)
	}
	for _, data := range document.Data {
		if data.Type != "primary" {
			continue
		}
		href := strings.TrimSpace(data.Location.Href)
		digest := strings.ToLower(strings.TrimSpace(data.Checksum.Value))
		switch {
		case !repodataHref.MatchString(href) || strings.Contains(href, ".."):
			return repomdFile{}, fmt.Errorf("the primary metadata is at %q, which is not a path below the repository", href)
		case data.Checksum.Type != "sha256" || !sha256Hex.MatchString(digest):
			return repomdFile{}, fmt.Errorf("the primary metadata has no SHA-256 digest (%s)", data.Checksum.Type)
		case data.Size <= 0 || data.OpenSize < 0:
			return repomdFile{}, errors.New("the primary metadata declares no size")
		}
		return repomdFile{href: href, size: data.Size, openSize: data.OpenSize, sha256: digest}, nil
	}
	return repomdFile{}, errors.New("no primary metadata listed")
}

// primary streams one repository's primary metadata: the body is hashed and
// counted as it is read, decompressed, and parsed element by element, so
// neither the 16 MB file nor its 185 MB of XML is ever held. The entries
// count only once the size and the digest are what repomd.xml said.
//
// Two bounds apply, as for an apt index: the size repomd.xml declares
// before decompression, and its open-size after, so a file that expands
// past what it declared is cut off at that byte rather than parsed to the
// end.
func (f *fetcher) primary(plan rpmPlan) ([]Entry, error) {
	f.pocketBytes = 0
	url := plan.baseURL + "/" + plan.primary.href
	body, err := f.get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }() // the body has been read or the fetch failed
	hasher := sha256.New()
	bounded := &boundedReader{reader: body, limit: plan.primary.size + 1}
	counted := &countingReader{reader: io.TeeReader(bounded, hasher), onRead: func(count int64) {
		f.doneBytes += count
		f.pocketBytes += count
		if f.progress != nil {
			f.progress(f.doneBytes, f.totalBytes)
		}
	}}
	text, closeText, err := decompressMetadata(plan.primary.href, bufio.NewReaderSize(counted, 256<<10))
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %w", url, errChanged, err)
	}
	defer closeText()
	openLimit := plan.primary.openSize
	if openLimit == 0 {
		openLimit = f.maxBodyBytes
	}
	opened := &boundedReader{reader: text, limit: openLimit + 1}
	entries, err := reducePrimary(opened)
	if err == nil {
		// What follows the document is whitespace at most, and it counts
		// toward both bounds like the rest.
		_, err = io.Copy(io.Discard, opened)
	}
	if err != nil {
		if f.ctx.Err() != nil {
			return nil, f.ctx.Err()
		}
		if errors.Is(err, errTooLarge) || counted.count > plan.primary.size {
			return nil, fmt.Errorf("%s: %w", url, errTooLarge)
		}
		// A cut download fails in the decompressor or the parser before
		// the digest is ever compared.
		return nil, fmt.Errorf("%s: %w: %w", url, errChanged, err)
	}
	// A decompressor may stop before the last bytes of its input.
	if _, err := io.Copy(io.Discard, counted); err != nil && !errors.Is(err, errTooLarge) {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if counted.count > plan.primary.size {
		return nil, fmt.Errorf("%s: %w", url, errTooLarge)
	}
	if counted.count != plan.primary.size || hex.EncodeToString(hasher.Sum(nil)) != plan.primary.sha256 {
		return nil, fmt.Errorf("%s: %w", url, errChanged)
	}
	return entries, nil
}

// decompressMetadata returns the text of a metadata file by its name's
// extension, and what releases the decompressor.
func decompressMetadata(href string, reader io.Reader) (io.Reader, func(), error) {
	switch {
	case strings.HasSuffix(href, ".zst"):
		decoder, err := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(maxZstdWindow))
		if err != nil {
			return nil, nil, err
		}
		return decoder, decoder.Close, nil
	case strings.HasSuffix(href, ".xz"):
		decoder, err := xz.NewReader(reader)
		return decoder, func() {}, err
	case strings.HasSuffix(href, ".gz"):
		decoder, err := gzip.NewReader(reader)
		return decoder, func() {}, err
	default:
		return reader, func() {}, nil
	}
}

// rpmPackage is what the index keeps of one <package> while it is read.
type rpmPackage struct {
	name, arch, summary string
	epoch, version, rel string
	isRPM, hasVersion   bool
}

// reducePrimary reads primary.xml as a stream of tokens and keeps, of each
// package built for this architecture or for none, its name, its version
// and its summary. A name listed more than once keeps its newest version,
// as dnf would install it: Fedora 44's own repository lists two
// rubygem-bundler, 2.6.9 and 4.0.3.
//
// RawToken is used rather than Token because nothing here needs namespaces
// resolved, and the provides, requires and file lists below <format> are
// most of the 185 MB; they are passed over a token at a time without being
// kept.
func reducePrimary(reader io.Reader) ([]Entry, error) {
	decoder := xml.NewDecoder(reader)
	byName := map[string]rpmPackage{}
	var current rpmPackage
	depth := 0
	reading := "" // the element of the package whose text is being read
	isMetadata := false
	for {
		token, err := decoder.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch element := token.(type) {
		case xml.StartElement:
			depth++
			reading = ""
			switch {
			case depth == 1:
				isMetadata = element.Name.Local == "metadata"
			case depth == 2 && element.Name.Local == "package":
				current = rpmPackage{isRPM: attribute(element, "type") == "rpm"}
			case depth == 3 && element.Name.Local == "version":
				current.epoch, current.version, current.rel = attribute(element, "epoch"), attribute(element, "ver"), attribute(element, "rel")
				current.hasVersion = true
			case depth == 3:
				reading = element.Name.Local
			}
		case xml.CharData:
			switch reading {
			case "name":
				current.name += string(element)
			case "arch":
				current.arch += string(element)
			case "summary":
				current.summary += string(element)
			}
		case xml.EndElement:
			reading = ""
			if depth == 2 && element.Name.Local == "package" {
				keepNewest(byName, current)
			}
			depth--
			if depth < 0 {
				return nil, errors.New("an element closes that never opened")
			}
		}
	}
	if depth != 0 {
		return nil, io.ErrUnexpectedEOF
	}
	if !isMetadata {
		return nil, errors.New("not primary metadata: its root element is not <metadata>")
	}
	entries := make([]Entry, 0, len(byName))
	for _, kept := range byName {
		entries = append(entries, Entry{
			Name:        kept.name,
			Version:     displayEVR(kept.epoch, kept.version, kept.rel),
			Description: oneLine(kept.summary),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// keepNewest records a package read in full, unless it is built for another
// architecture, is not a binary rpm, or is older than one of its name
// already kept.
func keepNewest(byName map[string]rpmPackage, read rpmPackage) {
	read.name, read.arch = oneLine(read.name), strings.TrimSpace(read.arch)
	if !read.isRPM || !read.hasVersion || read.name == "" {
		return
	}
	if read.arch != distro.FedoraArch && read.arch != "noarch" {
		return
	}
	if kept, isKept := byName[read.name]; isKept && compareEVR(kept.epoch, kept.version, kept.rel, read.epoch, read.version, read.rel) >= 0 {
		return
	}
	byName[read.name] = read
}

// attribute returns the value of an element's attribute, or "".
func attribute(element xml.StartElement, name string) string {
	for _, attr := range element.Attr {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

// displayEVR renders a version the way rpm prints one: the epoch only when
// there is one, as in 2:9.2.240-1.fc44.
func displayEVR(epoch, version, release string) string {
	text := oneLine(version) + "-" + oneLine(release)
	if epoch = strings.TrimSpace(epoch); epoch != "" && epoch != "0" {
		text = oneLine(epoch) + ":" + text
	}
	return text
}

// compareEVR orders two epoch, version and release triples as rpm does: by
// epoch, a missing one being 0, then by version, then by release, each
// compared with rpmvercmp.
func compareEVR(epochA, versionA, releaseA, epochB, versionB, releaseB string) int {
	if epochA == "" {
		epochA = "0"
	}
	if epochB == "" {
		epochB = "0"
	}
	if order := rpmvercmp(epochA, epochB); order != 0 {
		return order
	}
	if order := rpmvercmp(versionA, versionB); order != 0 {
		return order
	}
	return rpmvercmp(releaseA, releaseB)
}

// rpmvercmp compares two version strings as rpm's rpmvercmp does, of which
// this is a translation: both are cut into runs of digits and runs of
// letters, anything else only separating them. Digit runs compare as
// numbers and beat letter runs; a tilde sorts before everything, the end of
// the string included (1.0~rc1 is older than 1.0); a caret sorts after the
// end but before anything else (1.0^git1 is newer than 1.0, older than
// 1.0.1).
func rpmvercmp(a, b string) int {
	if a == b {
		return 0
	}
	isSeparator := func(character byte) bool {
		return !isASCIIAlphanumeric(character) && character != '~' && character != '^'
	}
	for len(a) > 0 || len(b) > 0 {
		for len(a) > 0 && isSeparator(a[0]) {
			a = a[1:]
		}
		for len(b) > 0 && isSeparator(b[0]) {
			b = b[1:]
		}
		if strings.HasPrefix(a, "~") || strings.HasPrefix(b, "~") {
			if !strings.HasPrefix(a, "~") {
				return 1
			}
			if !strings.HasPrefix(b, "~") {
				return -1
			}
			a, b = a[1:], b[1:]
			continue
		}
		if strings.HasPrefix(a, "^") || strings.HasPrefix(b, "^") {
			switch {
			case a == "":
				return -1
			case b == "":
				return 1
			case !strings.HasPrefix(a, "^"):
				return 1
			case !strings.HasPrefix(b, "^"):
				return -1
			}
			a, b = a[1:], b[1:]
			continue
		}
		if a == "" || b == "" {
			break
		}
		numeric := isASCIIDigit(a[0])
		runA, restA := leadingRun(a, numeric)
		runB, restB := leadingRun(b, numeric)
		if runB == "" {
			// The runs are of different kinds, and digits are newer.
			if numeric {
				return 1
			}
			return -1
		}
		if numeric {
			runA, runB = strings.TrimLeft(runA, "0"), strings.TrimLeft(runB, "0")
			if len(runA) != len(runB) {
				if len(runA) > len(runB) {
					return 1
				}
				return -1
			}
		}
		if order := strings.Compare(runA, runB); order != 0 {
			return order
		}
		a, b = restA, restB
	}
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	default:
		return 1
	}
}

// leadingRun splits text after its leading run of digits, or of letters.
func leadingRun(text string, digits bool) (run, rest string) {
	end := 0
	for end < len(text) && (digits && isASCIIDigit(text[end]) || !digits && isASCIILetter(text[end])) {
		end++
	}
	return text[:end], text[end:]
}

func isASCIIDigit(character byte) bool { return character >= '0' && character <= '9' }

func isASCIILetter(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func isASCIIAlphanumeric(character byte) bool {
	return isASCIIDigit(character) || isASCIILetter(character)
}
