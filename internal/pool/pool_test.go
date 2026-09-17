package pool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frostroot/internal/deb"
	"frostroot/internal/deb/debtest"
	"frostroot/internal/recipe"
)

// fileContent is what a fake .deb file in these tests holds; the pool only
// hashes it, except where noted.
func fileContent(name string) []byte { return []byte("deb " + name + " " + strings.Repeat("x", 100)) }

// entryFor returns an Entry for a fake file with fileContent, at the pool
// path Ubuntu would use.
func entryFor(packageName, version string) Entry {
	fileName := packageName + "_" + version + "_amd64.deb"
	content := fileContent(fileName)
	digest := sha256.Sum256(content)
	return Entry{
		Package:  packageName,
		Version:  version,
		Arch:     "amd64",
		FileName: fileName,
		URLPath:  "pool/main/" + packageName[:1] + "/" + packageName + "/" + fileName,
		Size:     int64(len(content)),
		SHA256:   hex.EncodeToString(digest[:]),
	}
}

// writeEntry puts the correct file for entry into dir.
func writeEntry(t *testing.T, dir string, entry Entry) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, entry.FileName), fileContent(entry.FileName), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lockFor returns a lock with checksums naming entries.
func lockFor(entries ...Entry) recipe.Lockfile {
	lock := recipe.Lockfile{Version: 1, Distro: "ubuntu", Release: "24.04", Suite: "noble", Arch: "amd64", Mirror: "http://archive.ubuntu.com/ubuntu"}
	for _, entry := range entries {
		lock.Packages = append(lock.Packages, recipe.LockPackage{Name: entry.Package, Version: entry.Version, Arch: entry.Arch, SHA256: entry.SHA256, Size: entry.Size, Filename: entry.URLPath})
	}
	return lock
}

// fakeMirror serves fake .deb files by URL path and counts requests.
type fakeMirror struct {
	server   *httptest.Server
	mutex    sync.Mutex
	files    map[string][]byte // URL path (without leading slash) to content
	statuses map[string][]int  // URL path to the statuses to answer, in order, before serving normally
	requests []string
}

func newFakeMirror(t *testing.T, entries ...Entry) *fakeMirror {
	t.Helper()
	mirror := &fakeMirror{files: map[string][]byte{}, statuses: map[string][]int{}}
	for _, entry := range entries {
		mirror.files[entry.URLPath] = fileContent(entry.FileName)
	}
	mirror.server = httptest.NewServer(http.HandlerFunc(mirror.serve))
	t.Cleanup(mirror.server.Close)
	return mirror
}

func (m *fakeMirror) serve(writer http.ResponseWriter, request *http.Request) {
	m.mutex.Lock()
	urlPath := strings.TrimPrefix(request.URL.Path, "/")
	m.requests = append(m.requests, urlPath)
	if statuses := m.statuses[urlPath]; len(statuses) > 0 {
		status := statuses[0]
		m.statuses[urlPath] = statuses[1:]
		m.mutex.Unlock()
		writer.WriteHeader(status)
		return
	}
	content, found := m.files[urlPath]
	m.mutex.Unlock()
	if !found {
		http.NotFound(writer, request)
		return
	}
	_, _ = writer.Write(content) // a client that went away is not the test's concern
}

func (m *fakeMirror) requestCount() int {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return len(m.requests)
}

// noSleep skips retry delays.
func noSleep(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }

// fetchOptions returns options fetching entries from mirror into a new pool
// directory.
func fetchOptions(t *testing.T, mirror *fakeMirror, entries ...Entry) FetchOptions {
	t.Helper()
	return FetchOptions{Dir: filepath.Join(t.TempDir(), "vendor", "debs"), Entries: entries, MirrorURL: mirror.server.URL, Sleep: noSleep}
}

// assertPoolHolds fails unless dir holds exactly the entries' files, correct,
// and no temporary files.
func assertPoolHolds(t *testing.T, dir string, entries ...Entry) {
	t.Helper()
	status, err := Verify(dir, entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Complete() || len(status.Extra) != 0 {
		t.Errorf("pool status = %+v, want every entry present and nothing else", status)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %q", leftovers)
	}
}

func TestManifest(t *testing.T) {
	curl, git := entryFor("curl", "8.5.0-2ubuntu10.13"), entryFor("git", "1%3a2.43.0-1ubuntu7.3")
	entries, err := Manifest(lockFor(curl, git))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(entries, []Entry{curl, git}) {
		t.Errorf("Manifest =\n%+v\nwant\n%+v", entries, []Entry{curl, git})
	}
	if TotalSize(entries) != curl.Size+git.Size {
		t.Errorf("TotalSize = %d", TotalSize(entries))
	}
}

func TestManifestRefusals(t *testing.T) {
	uppercaseDigest := lockFor(entryFor("curl", "1"))
	uppercaseDigest.Packages[0].SHA256 = strings.ToUpper(uppercaseDigest.Packages[0].SHA256)
	if entries, err := Manifest(uppercaseDigest); err != nil || entries[0].SHA256 != strings.ToLower(uppercaseDigest.Packages[0].SHA256) {
		t.Errorf("an uppercase digest should be accepted and lowercased: %v, %+v", err, entries)
	}

	version2 := lockFor(entryFor("curl", "1"))
	version2.Version = 2
	oldLock := lockFor(entryFor("curl", "1"))
	oldLock.Packages[0].SHA256, oldLock.Packages[0].Size, oldLock.Packages[0].Filename = "", 0, ""
	duplicate := lockFor(entryFor("curl", "1"), entryFor("curl", "1"))
	duplicate.Packages[1].Name = "curl-copy"
	testCases := []struct {
		name      string
		lock      recipe.Lockfile
		wantError error
	}{
		{name: "format version 2", lock: version2, wantError: ErrBadLock},
		{name: "frostroot 0.3 lock", lock: oldLock, wantError: ErrNoChecksums},
		{name: "two packages with one file name", lock: duplicate, wantError: ErrBadLock},
	}
	for _, badFileName := range []string{"/etc/passwd", "../escape.deb", "pool/../../escape.deb", "pool/main/c/curl/notadeb", ".deb", "pool/./x.deb"} {
		lock := lockFor(entryFor("curl", "1"))
		lock.Packages[0].Filename = badFileName
		testCases = append(testCases, struct {
			name      string
			lock      recipe.Lockfile
			wantError error
		}{name: "filename " + badFileName, lock: lock, wantError: ErrBadLock})
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := Manifest(testCase.lock); !errors.Is(err, testCase.wantError) {
				t.Errorf("Manifest error = %v, want %v", err, testCase.wantError)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "debs")
	present, missing, wrongContent, wrongSize := entryFor("present", "1"), entryFor("missing", "1"), entryFor("content", "1"), entryFor("size", "1")
	writeEntry(t, dir, present)
	if err := os.WriteFile(filepath.Join(dir, wrongContent.FileName), []byte(strings.Repeat("y", int(wrongContent.Size))), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, wrongSize.FileName), []byte("short"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{"stale_0.9_amd64.deb", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, extra), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var checks []int
	status, err := Verify(dir, []Entry{present, missing, wrongContent, wrongSize}, func(checked, total int) {
		if total != 4 {
			t.Errorf("total = %d, want 4", total)
		}
		checks = append(checks, checked)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Present) != 1 || status.Present[0].Package != "present" {
		t.Errorf("Present = %+v", status.Present)
	}
	if len(status.Missing) != 1 || status.Missing[0].Package != "missing" {
		t.Errorf("Missing = %+v", status.Missing)
	}
	if len(status.Corrupt) != 2 {
		t.Errorf("Corrupt = %+v, want the wrong content and the wrong size", status.Corrupt)
	}
	if !slices.Equal(status.Extra, []string{"stale_0.9_amd64.deb"}) {
		t.Errorf("Extra = %q, want only the stale .deb", status.Extra)
	}
	if status.Complete() {
		t.Error("Complete() = true with missing and corrupt files")
	}
	if !slices.Equal(checks, []int{1, 2, 3, 4}) {
		t.Errorf("progress = %v", checks)
	}
	description := status.Describe(2)
	if !strings.Contains(description, "and 1 more") || strings.Count(description, ".deb") != 2 {
		t.Errorf("Describe(2) = %q, want two names and a count", description)
	}
	if status.Describe(10) == "" || strings.Contains(status.Describe(10), "more") {
		t.Errorf("Describe(10) = %q", status.Describe(10))
	}
}

func TestVerifyMissingDirectory(t *testing.T) {
	entry := entryFor("curl", "1")
	status, err := Verify(filepath.Join(t.TempDir(), "nowhere"), []Entry{entry}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Missing) != 1 || !strings.Contains(status.Describe(10), "curl_1_amd64.deb (missing)") {
		t.Errorf("status = %+v", status)
	}
	if complete, err := Verify(t.TempDir(), nil, nil); err != nil || !complete.Complete() || complete.Describe(10) != "" {
		t.Errorf("an empty pool for an empty manifest is complete: %+v, %v", complete, err)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	keep := entryFor("keep", "1")
	writeEntry(t, dir, keep)
	for _, name := range []string{"old_1_amd64.deb", "older_1_amd64.deb", "README"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := Prune(dir, []Entry{keep})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []string{"old_1_amd64.deb", "older_1_amd64.deb"}) {
		t.Errorf("removed = %q", removed)
	}
	remaining, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range remaining {
		names = append(names, entry.Name())
	}
	if !slices.Equal(names, []string{"README", keep.FileName}) {
		t.Errorf("remaining = %q, want the kept package and the unrelated file", names)
	}
}

func TestFetchFreshPool(t *testing.T) {
	entries := []Entry{entryFor("curl", "1"), entryFor("git", "2"), entryFor("tzdata", "3")}
	mirror := newFakeMirror(t, entries...)
	options := fetchOptions(t, mirror, entries...)
	var lastDone, lastTotal int64
	var finished []string
	options.OnDownloaded = func(done, total int64) {
		if done < lastDone {
			t.Errorf("progress went backwards: %d after %d", done, lastDone)
		}
		lastDone, lastTotal = done, total
	}
	options.OnFileDone = func(entry Entry, sourceURL string) {
		finished = append(finished, entry.Package)
		if !strings.HasPrefix(sourceURL, mirror.server.URL+"/pool/main/") {
			t.Errorf("source URL = %q, want the mirror", sourceURL)
		}
	}
	summary, err := Fetch(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	assertPoolHolds(t, options.Dir, entries...)
	if summary.Fetched != 3 || summary.Present != 0 || summary.Replaced != 0 || summary.FetchedBytes != TotalSize(entries) {
		t.Errorf("summary = %+v", summary)
	}
	if lastDone != TotalSize(entries) || lastTotal != TotalSize(entries) {
		t.Errorf("final progress = %d / %d, want %d / %d", lastDone, lastTotal, TotalSize(entries), TotalSize(entries))
	}
	slices.Sort(finished)
	if !slices.Equal(finished, []string{"curl", "git", "tzdata"}) {
		t.Errorf("finished = %q", finished)
	}
	if mirror.requestCount() != 3 {
		t.Errorf("requests = %d, want one per file", mirror.requestCount())
	}
}

func TestFetchResumesAndReplacesCorruptFiles(t *testing.T) {
	present, corrupt, missing := entryFor("present", "1"), entryFor("corrupt", "1"), entryFor("missing", "1")
	mirror := newFakeMirror(t, present, corrupt, missing)
	options := fetchOptions(t, mirror, present, corrupt, missing)
	writeEntry(t, options.Dir, present)
	if err := os.WriteFile(filepath.Join(options.Dir, corrupt.FileName), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.Dir, "stale_1_amd64.deb"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	var replaced []string
	options.OnReplaced = func(entry Entry) { replaced = append(replaced, entry.Package) }
	summary, err := Fetch(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Present != 1 || summary.Fetched != 2 || summary.Replaced != 1 || !slices.Equal(summary.Extra, []string{"stale_1_amd64.deb"}) {
		t.Errorf("summary = %+v", summary)
	}
	if !slices.Equal(replaced, []string{"corrupt"}) {
		t.Errorf("replaced = %q", replaced)
	}
	if mirror.requestCount() != 2 {
		t.Errorf("requests = %d, want only the missing and the corrupt file", mirror.requestCount())
	}
	if err := os.Remove(filepath.Join(options.Dir, "stale_1_amd64.deb")); err != nil {
		t.Fatal(err)
	}
	assertPoolHolds(t, options.Dir, present, corrupt, missing)

	// Nothing to do the second time: no request at all.
	summary, err = Fetch(context.Background(), options)
	if err != nil || summary.Fetched != 0 || summary.Present != 3 || mirror.requestCount() != 2 {
		t.Errorf("second fetch: summary %+v, err %v, requests %d", summary, err, mirror.requestCount())
	}
}

func TestFetchFallsBackWhenTheMirrorDroppedAFile(t *testing.T) {
	dropped, kept := entryFor("dropped", "1"), entryFor("kept", "1")
	mirror := newFakeMirror(t, kept)
	librarian := newFakeMirror(t, dropped)
	librarian.files["+files/"+dropped.FileName] = fileContent(dropped.FileName)
	options := fetchOptions(t, mirror, dropped, kept)
	options.Fallback = func(entry Entry) string { return librarian.server.URL + "/+files/" + entry.FileName }
	sources := map[string]string{}
	options.OnFileDone = func(entry Entry, sourceURL string) { sources[entry.Package] = sourceURL }
	if _, err := Fetch(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	assertPoolHolds(t, options.Dir, dropped, kept)
	if !strings.HasPrefix(sources["dropped"], librarian.server.URL) || !strings.HasPrefix(sources["kept"], mirror.server.URL) {
		t.Errorf("sources = %v", sources)
	}
}

func TestFetchNotFoundAnywhere(t *testing.T) {
	gone := entryFor("gone", "1")
	mirror := newFakeMirror(t)
	options := fetchOptions(t, mirror, gone)
	options.Fallback = fallbackTo(mirror)
	_, err := Fetch(context.Background(), options)
	var fetchErr *FetchError
	if !errors.Is(err, ErrNotFound) || !errors.As(err, &fetchErr) || fetchErr.Entry.Package != "gone" {
		t.Fatalf("err = %v, want ErrNotFound naming the package", err)
	}
	if !strings.Contains(err.Error(), "nor") || !strings.Contains(err.Error(), "+files/gone_1_amd64.deb") {
		t.Errorf("err = %v, want both URLs", err)
	}
	if mirror.requestCount() != 2 {
		t.Errorf("requests = %d, want the mirror then the fallback, no retries for 404", mirror.requestCount())
	}
	assertPoolHolds(t, options.Dir)
}

// fallbackTo returns a Fallback pointing at mirror's +files path, shaped
// like LaunchpadURL.
func fallbackTo(mirror *fakeMirror) func(Entry) string {
	return func(entry Entry) string { return mirror.server.URL + "/+files/" + entry.FileName }
}

func TestFetchWithoutFallbackReportsTheMirrorOnly(t *testing.T) {
	gone := entryFor("gone", "1")
	mirror := newFakeMirror(t)
	options := fetchOptions(t, mirror, gone)
	_, err := Fetch(context.Background(), options)
	if !errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), "nor") || mirror.requestCount() != 1 {
		t.Fatalf("err = %v, requests = %d", err, mirror.requestCount())
	}
}

func TestFetchMismatchLeavesNothing(t *testing.T) {
	entry := entryFor("curl", "1")
	testCases := []struct {
		name    string
		content []byte
	}{
		{name: "same size, other content", content: []byte(strings.Repeat("z", int(entry.Size)))},
		{name: "other size", content: []byte("tiny")},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			mirror := newFakeMirror(t)
			mirror.files[entry.URLPath] = testCase.content
			options := fetchOptions(t, mirror, entry)
			_, err := Fetch(context.Background(), options)
			if !errors.Is(err, ErrMismatch) || !strings.Contains(err.Error(), "curl 1") {
				t.Fatalf("err = %v, want ErrMismatch naming the package", err)
			}
			if mirror.requestCount() != 1 {
				t.Errorf("requests = %d, want no retry for a mismatch", mirror.requestCount())
			}
			assertPoolHolds(t, options.Dir)
		})
	}
}

func TestFetchRetriesServerErrorsOnce(t *testing.T) {
	entry := entryFor("curl", "1")
	t.Run("recovers", func(t *testing.T) {
		mirror := newFakeMirror(t, entry)
		mirror.statuses[entry.URLPath] = []int{http.StatusInternalServerError}
		options := fetchOptions(t, mirror, entry)
		if _, err := Fetch(context.Background(), options); err != nil {
			t.Fatal(err)
		}
		assertPoolHolds(t, options.Dir, entry)
		if mirror.requestCount() != 2 {
			t.Errorf("requests = %d, want a retry", mirror.requestCount())
		}
	})
	t.Run("gives up", func(t *testing.T) {
		mirror := newFakeMirror(t, entry)
		mirror.statuses[entry.URLPath] = []int{http.StatusBadGateway, http.StatusBadGateway}
		options := fetchOptions(t, mirror, entry)
		_, err := Fetch(context.Background(), options)
		if err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), mirror.server.URL) {
			t.Fatalf("err = %v, want the status and the URL", err)
		}
		if mirror.requestCount() != 2 {
			t.Errorf("requests = %d, want exactly one retry", mirror.requestCount())
		}
		assertPoolHolds(t, options.Dir)
	})
}

func TestFetchCanceledRemovesPartialFiles(t *testing.T) {
	entry := entryFor("big", "1")
	entry.Size = 1 << 20
	entry.SHA256 = strings.Repeat("0", 64)
	released := make(chan struct{})
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", "1048576")
		_, _ = writer.Write(make([]byte, 4096)) // half a file, then wait for the cancel
		if flusher, canFlush := writer.(http.Flusher); canFlush {
			flusher.Flush()
		}
		started <- struct{}{}
		<-released
	}))
	t.Cleanup(func() { close(released); server.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	options := FetchOptions{Dir: t.TempDir(), Entries: []Entry{entry}, MirrorURL: server.URL, Sleep: noSleep}
	result := make(chan error, 1)
	go func() {
		_, err := Fetch(ctx, options)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the download never started")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Fetch did not return after cancel")
	}
	assertPoolHolds(t, options.Dir)
}

func TestFetchManyFilesWithSeveralWorkers(t *testing.T) {
	var entries []Entry
	for i := range 25 {
		entries = append(entries, entryFor("pkg"+strings.Repeat("a", i%5)+string(rune('a'+i)), "1"))
	}
	mirror := newFakeMirror(t, entries...)
	options := fetchOptions(t, mirror, entries...)
	options.Workers = 4
	var progressCalls atomic.Int64
	var lastDone int64
	options.OnDownloaded = func(done, total int64) {
		progressCalls.Add(1)
		if done < lastDone || done > total {
			t.Errorf("progress %d after %d, total %d", done, lastDone, total)
		}
		lastDone = done
	}
	summary, err := Fetch(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	assertPoolHolds(t, options.Dir, entries...)
	if summary.Fetched != 25 || lastDone != TotalSize(entries) {
		t.Errorf("summary = %+v, final progress %d", summary, lastDone)
	}
}

func TestStageBuildsARepository(t *testing.T) {
	poolDir := t.TempDir()
	hello := debtest.Build(t, poolDir, "hello_2.10-3build1_amd64.deb", ".zst", debtest.Control("hello", "2.10-3build1", "amd64"))
	adduser := debtest.Build(t, poolDir, "adduser_3.137ubuntu1_all.deb", ".gz", debtest.Control("adduser", "3.137ubuntu1", "all"))
	var entries []Entry
	for _, path := range []string{hello, adduser} {
		size, digest, err := deb.SHA256File(path)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, Entry{Package: strings.SplitN(filepath.Base(path), "_", 2)[0], FileName: filepath.Base(path), Size: size, SHA256: digest})
	}
	// A tightened mode must not survive into the repository.
	if err := os.Chmod(hello, 0o600); err != nil {
		t.Fatal(err)
	}
	repositoryDir := filepath.Join(t.TempDir(), "work", "pool")
	var progress []int64
	release := deb.FlatRelease{Suite: "noble", Arch: "amd64", Date: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	if err := Stage(poolDir, entries, repositoryDir, release, func(done, total int64) {
		if total != TotalSize(entries) {
			t.Errorf("total = %d", total)
		}
		progress = append(progress, done)
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(progress, []int64{entries[0].Size, TotalSize(entries)}) {
		t.Errorf("progress = %v", progress)
	}
	for _, entry := range entries {
		info, err := os.Stat(filepath.Join(repositoryDir, entry.FileName))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 || info.Size() != entry.Size {
			t.Errorf("%s: mode %v, size %d", entry.FileName, info.Mode().Perm(), info.Size())
		}
	}
	if dirInfo, err := os.Stat(repositoryDir); err != nil || dirInfo.Mode().Perm() != 0o755 {
		t.Errorf("repository dir: %v, %v", dirInfo, err)
	}
	packages, err := os.ReadFile(filepath.Join(repositoryDir, deb.PackagesFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, wantText := range []string{"Package: adduser\n", "Package: hello\n", "Filename: ./hello_2.10-3build1_amd64.deb\n", "SHA256: " + entries[0].SHA256 + "\n"} {
		if !strings.Contains(string(packages), wantText) {
			t.Errorf("Packages lacks %q:\n%s", wantText, packages)
		}
	}
	releaseText, err := os.ReadFile(filepath.Join(repositoryDir, deb.ReleaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(releaseText), "Codename: noble\n") {
		t.Errorf("Release:\n%s", releaseText)
	}
	// Staging again replaces what is there.
	if err := Stage(poolDir, entries, repositoryDir, release, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchpadURL(t *testing.T) {
	entry := entryFor("git", "1%3a2.43.0-1ubuntu7.3")
	if got := LaunchpadURL(entry); got != "https://launchpad.net/ubuntu/+archive/primary/+files/git_1%3a2.43.0-1ubuntu7.3_amd64.deb" {
		t.Errorf("LaunchpadURL = %q", got)
	}
}
