package index

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"frostroot/internal/distro"
)

// cacheFormat is the first field of a cache file's header. A file that
// starts with anything else was written by another frostroot, or by nothing
// at all, and is fetched again.
const cacheFormat = "frostroot-index 1"

// entryFields is how many tab-separated fields an entry line has.
const entryFields = 5

// cacheHeader is what a cache file says of itself: what it is an index of,
// where it came from, and when.
type cacheHeader struct {
	suite, arch, archiveURL, components string
	fetched                             time.Time
}

// headerFor is the header a cache file must have to serve options.
func headerFor(options Options, fetched time.Time) cacheHeader {
	archiveURL := options.Release.ArchiveURL
	if options.Mirror != "" {
		archiveURL = options.Mirror
	}
	return cacheHeader{
		suite:      options.Release.Suite,
		arch:       distro.SupportedArch,
		archiveURL: strings.TrimRight(archiveURL, "/"),
		components: strings.Join(options.Release.Components, ","),
		fetched:    fetched,
	}
}

func (h cacheHeader) line() string {
	return strings.Join([]string{cacheFormat, h.suite, h.arch, h.archiveURL, h.components, h.fetched.UTC().Format(time.RFC3339)}, "\t")
}

// sameSource reports whether two headers describe the same index, whenever
// each was fetched.
func (h cacheHeader) sameSource(other cacheHeader) bool {
	h.fetched, other.fetched = time.Time{}, time.Time{}
	return h == other
}

// parseHeader reads a header line.
func parseHeader(line string) (cacheHeader, error) {
	fields := strings.Split(line, "\t")
	if len(fields) != 6 || fields[0] != cacheFormat {
		return cacheHeader{}, errors.New("not a frostroot index of this format")
	}
	fetched, err := time.Parse(time.RFC3339, fields[5])
	if err != nil {
		return cacheHeader{}, fmt.Errorf("unreadable fetch time: %w", err)
	}
	return cacheHeader{suite: fields[1], arch: fields[2], archiveURL: fields[3], components: fields[4], fetched: fetched}, nil
}

// readCache returns the cached index for options, or nil: no cache
// directory, no file, a file for another mirror or other components, or a
// file that does not parse. The last is deleted, never trusted; a file for
// another source is left for the fetch to replace.
func readCache(options Options) *Index {
	path := cachePath(options)
	if path == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }() // read only; nothing is lost
	header, entries, err := parseCache(file)
	if err != nil {
		_ = file.Close() // so that the file can go, where an open one cannot
		removeQuietly(path)
		return nil
	}
	if !header.sameSource(headerFor(options, time.Time{})) {
		return nil
	}
	return newIndex(header.suite, header.fetched, entries, options.Now)
}

// parseCache reads a whole cache file.
func parseCache(file *os.File) (cacheHeader, []Entry, error) {
	text, err := gzip.NewReader(bufio.NewReaderSize(file, 256<<10))
	if err != nil {
		return cacheHeader{}, nil, err
	}
	scanner := bufio.NewScanner(text)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	if !scanner.Scan() {
		return cacheHeader{}, nil, errors.New("empty index file")
	}
	header, err := parseHeader(scanner.Text())
	if err != nil {
		return cacheHeader{}, nil, err
	}
	var entries []Entry
	previousName := ""
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != entryFields || fields[0] <= previousName {
			return cacheHeader{}, nil, fmt.Errorf("unreadable entry after %q", previousName)
		}
		previousName = fields[0]
		entries = append(entries, Entry{Name: fields[0], Version: fields[1], Component: fields[2], Section: fields[3], Description: fields[4]})
	}
	if err := scanner.Err(); err != nil {
		return cacheHeader{}, nil, err
	}
	if len(entries) == 0 {
		return cacheHeader{}, nil, errors.New("index file without entries")
	}
	return header, entries, nil
}

// writeCache stores built for the next run: to a temporary file beside the
// final one, then renamed, so that a reader never sees half a file and an
// interrupted write leaves the old one.
func writeCache(options Options, built *Index) (err error) {
	path := cachePath(options)
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
	if _, err = buffered.WriteString(headerFor(options, built.fetched).line() + "\n"); err != nil {
		return err
	}
	for _, entry := range built.entries {
		if _, err = buffered.WriteString(strings.Join([]string{entry.Name, entry.Version, entry.Component, entry.Section, entry.Description}, "\t") + "\n"); err != nil {
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
