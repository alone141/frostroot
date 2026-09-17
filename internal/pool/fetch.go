package pool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"frostroot/internal/export"
)

// Defaults of Fetch.
const (
	DefaultWorkers        = 4
	defaultUserAgent      = "frostroot"
	retryDelay            = time.Second
	responseHeaderTimeout = 30 * time.Second
	copyBufferBytes       = 256 << 10
)

// Errors of a fetch. Compare with errors.Is; a *FetchError wraps them and
// names the file.
var (
	// ErrNotFound means every source answered 404 for the file.
	ErrNotFound = errors.New("not found")
	// ErrMismatch means a source served a file with another size or
	// checksum than the lock records.
	ErrMismatch = errors.New("size or checksum differs from the lock")
)

// FetchError says which file could not be fetched, from where, and why.
type FetchError struct {
	Entry Entry
	URL   string
	Err   error
}

func (e *FetchError) Error() string {
	return fmt.Sprintf("%s %s: %s: %v", e.Entry.Package, e.Entry.Version, e.URL, e.Err)
}

func (e *FetchError) Unwrap() error { return e.Err }

// FetchOptions configure Fetch. Callbacks may be nil; they are called from
// several goroutines but never concurrently with each other.
type FetchOptions struct {
	Dir       string  // the pool directory; created if missing
	Entries   []Entry // what the pool must hold
	MirrorURL string  // base URL the entries' URL paths are relative to
	// Fallback returns another URL to try for an entry the mirror does not
	// have (404), or "" for none. nil means no fallback.
	Fallback  func(Entry) string
	Client    *http.Client // nil: a default client honoring the proxy environment
	Workers   int          // concurrent downloads; <= 0 means DefaultWorkers
	UserAgent string       // "" means defaultUserAgent

	OnChecked    func(checked, total int)                  // Verify progress
	OnDownloaded func(doneBytes, totalBytes int64)         // download progress, in bytes still to fetch
	OnFileDone   func(entry Entry, sourceURL string)       // a file landed
	OnReplaced   func(entry Entry)                         // a corrupt file is about to be replaced
	OnVerified   func(status Status)                       // what the check found, before downloading
	Sleep        func(context.Context, time.Duration) bool // waits before a retry; nil means time.Sleep honoring ctx
}

// Summary is what a fetch did.
type Summary struct {
	Present      int      // files already there and correct
	Fetched      int      // files downloaded, replacements included
	Replaced     int      // corrupt files replaced
	FetchedBytes int64    // bytes downloaded
	Extra        []string // .deb files in the pool that the lock does not name
}

// Fetch makes options.Dir hold every entry: it verifies what is there,
// downloads what is missing or corrupt, and checks each download against
// the lock's size and SHA-256 before renaming it into place. Partial
// downloads never keep the final name. The first failure cancels the other
// downloads and is returned; a later run resumes.
func Fetch(ctx context.Context, options FetchOptions) (Summary, error) {
	if err := os.MkdirAll(options.Dir, 0o755); err != nil {
		return Summary{}, err
	}
	status, err := Verify(options.Dir, options.Entries, options.OnChecked)
	if err != nil {
		return Summary{}, err
	}
	if options.OnVerified != nil {
		options.OnVerified(status)
	}
	summary := Summary{Present: len(status.Present), Replaced: len(status.Corrupt), Extra: status.Extra}
	needed := append(append([]Entry(nil), status.Missing...), status.Corrupt...)
	if len(needed) == 0 {
		return summary, ctx.Err()
	}
	for _, corrupt := range status.Corrupt {
		if options.OnReplaced != nil {
			options.OnReplaced(corrupt)
		}
	}

	fetcher := newFetcher(options, TotalSize(needed))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan Entry)
	var workers sync.WaitGroup
	for range fetcher.workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for entry := range jobs {
				if ctx.Err() != nil {
					continue // drain: a failure or Ctrl-C already ended the run
				}
				if err := fetcher.fetchOne(ctx, entry); err != nil {
					fetcher.fail(err)
					cancel()
				}
			}
		}()
	}
	for _, entry := range needed {
		select {
		case jobs <- entry:
		case <-ctx.Done():
		}
	}
	close(jobs)
	workers.Wait()

	summary.Fetched = fetcher.fetched
	summary.FetchedBytes = fetcher.doneBytes
	if fetcher.firstErr != nil {
		return summary, fetcher.firstErr
	}
	return summary, ctx.Err()
}

// fetcher is the shared state of one Fetch.
type fetcher struct {
	options    FetchOptions
	client     *http.Client
	workers    int
	totalBytes int64

	mutex     sync.Mutex // guards everything below and the callbacks
	doneBytes int64
	fetched   int
	firstErr  error
}

func newFetcher(options FetchOptions, totalBytes int64) *fetcher {
	f := &fetcher{options: options, client: options.Client, workers: options.Workers, totalBytes: totalBytes}
	if f.client == nil {
		transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
		if defaultTransport, isTransport := http.DefaultTransport.(*http.Transport); isTransport {
			transport = defaultTransport.Clone()
		}
		transport.ResponseHeaderTimeout = responseHeaderTimeout
		f.client = &http.Client{Transport: transport}
	}
	if f.workers <= 0 {
		f.workers = DefaultWorkers
	}
	if f.options.UserAgent == "" {
		f.options.UserAgent = defaultUserAgent
	}
	if f.options.Sleep == nil {
		f.options.Sleep = sleepUnlessCanceled
	}
	return f
}

// fail records the first error of the run.
func (f *fetcher) fail(err error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.firstErr == nil {
		f.firstErr = err
	}
}

// addProgress accounts for bytes just written and reports the total.
func (f *fetcher) addProgress(bytesWritten int64) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.doneBytes += bytesWritten
	if f.options.OnDownloaded != nil {
		f.options.OnDownloaded(f.doneBytes, f.totalBytes)
	}
}

// fetchOne downloads one entry from the mirror, or from the fallback when
// the mirror has dropped it, and puts it in place.
func (f *fetcher) fetchOne(ctx context.Context, entry Entry) error {
	mirrorURL := strings.TrimRight(f.options.MirrorURL, "/") + "/" + entry.URLPath
	sourceURL := mirrorURL
	err := f.downloadWithRetry(ctx, entry, mirrorURL)
	if errors.Is(err, ErrNotFound) && f.options.Fallback != nil {
		if fallbackURL := f.options.Fallback(entry); fallbackURL != "" {
			sourceURL = fallbackURL
			err = f.downloadWithRetry(ctx, entry, fallbackURL)
			if errors.Is(err, ErrNotFound) {
				return &FetchError{Entry: entry, URL: mirrorURL + " nor " + fallbackURL, Err: ErrNotFound}
			}
		}
	}
	if err != nil {
		return &FetchError{Entry: entry, URL: sourceURL, Err: err}
	}
	f.mutex.Lock()
	f.fetched++
	if f.options.OnFileDone != nil {
		f.options.OnFileDone(entry, sourceURL)
	}
	f.mutex.Unlock()
	return nil
}

// downloadWithRetry downloads entry from url, trying once more after a
// failure that may be transient: a network error or a server error. A 404
// and a mismatch are final.
func (f *fetcher) downloadWithRetry(ctx context.Context, entry Entry, url string) error {
	err := f.download(ctx, entry, url)
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrMismatch) || ctx.Err() != nil {
		return err
	}
	if !f.options.Sleep(ctx, retryDelay) {
		return err
	}
	return f.download(ctx, entry, url)
}

// download fetches url into a temporary file in the pool, checks its size and
// SHA-256 against entry, and renames it to the entry's file name. Any
// failure removes the temporary file.
func (f *fetcher) download(ctx context.Context, entry Entry, url string) (err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", f.options.UserAgent)
	response, err := f.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }() // the body has been read or the download failed; nothing is lost
	switch {
	case response.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case response.StatusCode != http.StatusOK:
		return fmt.Errorf("HTTP %s", response.Status)
	case response.ContentLength >= 0 && response.ContentLength != entry.Size:
		return fmt.Errorf("%w: the server offers %d bytes, the lock says %d", ErrMismatch, response.ContentLength, entry.Size)
	}

	temporary, err := export.CreateTemp(f.options.Dir, "."+entry.FileName+".*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		if err != nil {
			// Best effort: the download has already failed, and that error is
			// the one returned.
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	digest := sha256.New()
	written, err := io.CopyBuffer(&progressWriter{writer: io.MultiWriter(temporary, digest), report: f.addProgress}, io.LimitReader(response.Body, entry.Size+1), make([]byte, copyBufferBytes))
	if err != nil {
		return err
	}
	if written != entry.Size {
		return fmt.Errorf("%w: received %d bytes, the lock says %d", ErrMismatch, written, entry.Size)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != entry.SHA256 {
		return fmt.Errorf("%w: SHA-256 %s, the lock says %s", ErrMismatch, got, entry.SHA256)
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(f.options.Dir, entry.FileName))
}

// progressWriter reports every write to a function.
type progressWriter struct {
	writer io.Writer
	report func(bytesWritten int64)
}

func (w *progressWriter) Write(data []byte) (int, error) {
	written, err := w.writer.Write(data)
	if written > 0 {
		w.report(int64(written))
	}
	return written, err
}

// sleepUnlessCanceled waits for duration and reports whether it waited the
// whole time rather than being canceled.
func sleepUnlessCanceled(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// LaunchpadURL returns where Launchpad's librarian serves an Ubuntu package
// file. Launchpad keeps every file ever published to the Ubuntu archive, so
// it is the fallback for a package the archive has since dropped.
func LaunchpadURL(entry Entry) string {
	return "https://launchpad.net/ubuntu/+archive/primary/+files/" + entry.FileName
}
