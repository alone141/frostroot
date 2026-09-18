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
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// wheelContent is what a fake wheel in these tests holds; the pool only
// hashes it.
func wheelContent(fileName string) []byte {
	return []byte("wheel " + fileName + strings.Repeat("y", 60))
}

// wheelEntry returns an Entry for a fake wheel served by mirror, with no size,
// as a lock written from pip's installation report records it.
func wheelEntry(mirrorURL, name, version string) Entry {
	fileName := name + "-" + version + "-py3-none-any.whl"
	digest := sha256.Sum256(wheelContent(fileName))
	return Entry{
		Package:  name,
		Version:  version,
		FileName: fileName,
		URL:      mirrorURL + "/packages/" + fileName,
		SHA256:   hex.EncodeToString(digest[:]),
	}
}

// wheelLock returns a lock whose Python side names entries.
func wheelLock(entries ...Entry) recipe.Lockfile {
	lock := recipe.Lockfile{Version: 1, Distro: "ubuntu", Release: "24.04", Suite: "noble", Arch: "amd64", Mirror: sampleMirror}
	lock.Python = &recipe.LockPython{Requested: []string{"numpy"}, Venv: "/opt/frostroot/venv", Interpreter: "3.12.3", PipVersion: "24.3.1"}
	for _, entry := range entries {
		lock.PyPI = append(lock.PyPI, recipe.LockPyPI{Name: entry.Package, Version: entry.Version, SHA256: entry.SHA256, Size: entry.Size, Filename: entry.FileName, URL: entry.URL})
	}
	return lock
}

// wheelServer serves the wheels of entries at the paths their URLs name.
func wheelServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fileName := strings.TrimPrefix(request.URL.Path, "/packages/")
		if !strings.HasSuffix(fileName, ".whl") {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(wheelContent(fileName)) // a client that went away is not the test's concern
	}))
	t.Cleanup(server.Close)
	return server
}

func TestWheelManifest(t *testing.T) {
	numpy := wheelEntry("https://files.pythonhosted.org", "numpy", "2.5.3")
	six := wheelEntry("https://files.pythonhosted.org", "six", "1.17.0")
	entries, err := WheelManifest(wheelLock(numpy, six))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].FileName != "numpy-2.5.3-py3-none-any.whl" || entries[0].URL != numpy.URL {
		t.Fatalf("WheelManifest = %+v", entries)
	}
	if entries[0].Size != 0 {
		t.Errorf("Size = %d, want 0: pip's report gives none", entries[0].Size)
	}
	if got := entries[0].DownloadURL(); got != numpy.URL {
		t.Errorf("DownloadURL = %q, want the lock's whole address %q", got, numpy.URL)
	}

	// A lock with no Python side has no wheel pool, which is not an error.
	if entries, err := WheelManifest(lockFor(entryFor("curl", "1"))); err != nil || entries != nil {
		t.Errorf("WheelManifest of a lock without wheels = %+v, %v; want nothing", entries, err)
	}
}

func TestWheelManifestRefusals(t *testing.T) {
	good := wheelEntry("https://files.pythonhosted.org", "numpy", "2.5.3")
	testCases := []struct {
		name        string
		breakLock   func(*recipe.Lockfile)
		wantErr     error
		wantMessage string
	}{
		{
			name:      "another format version",
			breakLock: func(lock *recipe.Lockfile) { lock.Version = 2 },
			wantErr:   ErrBadLock, wantMessage: "format version",
		},
		{
			name:      "no checksum",
			breakLock: func(lock *recipe.Lockfile) { lock.PyPI[0].SHA256 = "" },
			wantErr:   ErrNoChecksums,
		},
		{
			name:      "no file name",
			breakLock: func(lock *recipe.Lockfile) { lock.PyPI[0].Filename = "" },
			wantErr:   ErrNoChecksums,
		},
		{
			name:      "no url",
			breakLock: func(lock *recipe.Lockfile) { lock.PyPI[0].URL = "" },
			wantErr:   ErrNoChecksums,
		},
		{
			// The file name becomes a path in the pool directory.
			name:      "path traversal",
			breakLock: func(lock *recipe.Lockfile) { lock.PyPI[0].Filename = "../../../etc/cron.d/evil.whl" },
			wantErr:   ErrBadLock, wantMessage: "plain file name",
		},
		{
			name:      "a directory in the file name",
			breakLock: func(lock *recipe.Lockfile) { lock.PyPI[0].Filename = "wheels/numpy.whl" },
			wantErr:   ErrBadLock, wantMessage: "plain file name",
		},
		{
			name:      "not a wheel",
			breakLock: func(lock *recipe.Lockfile) { lock.PyPI[0].Filename = "numpy-2.5.3.tar.gz" },
			wantErr:   ErrBadLock, wantMessage: ".whl",
		},
		{
			name: "two packages sharing a file name",
			breakLock: func(lock *recipe.Lockfile) {
				lock.PyPI = append(lock.PyPI, lock.PyPI[0])
				lock.PyPI[1].Name = "numpy-again"
			},
			wantErr: ErrBadLock, wantMessage: "share the file name",
		},
		{
			// The lock is the whole address, so it may not downgrade the
			// transport it was fetched over.
			name:      "not https",
			breakLock: func(lock *recipe.Lockfile) { lock.PyPI[0].URL = "http://files.pythonhosted.org/numpy.whl" },
			wantErr:   ErrBadLock, wantMessage: "not https",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			lock := wheelLock(good)
			testCase.breakLock(&lock)
			_, err := WheelManifest(lock)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("WheelManifest = %v, want %v", err, testCase.wantErr)
			}
			if testCase.wantMessage != "" && !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Errorf("WheelManifest = %q, want it to mention %q", err, testCase.wantMessage)
			}
		})
	}
}

func TestFetchWheelsWithoutSizes(t *testing.T) {
	server := wheelServer(t)
	entries := []Entry{wheelEntry(server.URL, "numpy", "2.5.3"), wheelEntry(server.URL, "six", "1.17.0")}
	dir := filepath.Join(t.TempDir(), "vendor", "wheels")
	summary, err := Fetch(context.Background(), FetchOptions{Dir: dir, Entries: entries, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Fetched != 2 {
		t.Errorf("Summary = %+v, want both wheels downloaded", summary)
	}
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(dir, entry.FileName))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != string(wheelContent(entry.FileName)) {
			t.Errorf("%s holds %q", entry.FileName, content)
		}
	}

	// A second run checks by checksum alone, since the lock records no size,
	// and downloads nothing.
	status, err := Verify(dir, entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Complete() || len(status.Present) != 2 {
		t.Errorf("Verify = %+v, want both present", status)
	}
}

func TestFetchWheelRefusesWrongContent(t *testing.T) {
	// Without a size, the checksum is the only thing that decides.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("not the wheel you asked for")) // see wheelServer
	}))
	t.Cleanup(server.Close)
	entry := wheelEntry(server.URL, "numpy", "2.5.3")
	dir := filepath.Join(t.TempDir(), "vendor", "wheels")
	_, err := Fetch(context.Background(), FetchOptions{Dir: dir, Entries: []Entry{entry}, Sleep: noSleep})
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("Fetch = %v, want a mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(dir, entry.FileName)); !os.IsNotExist(err) {
		t.Errorf("a file that did not match its checksum was kept: %v", err)
	}
}

func TestVerifyAndPruneWheels(t *testing.T) {
	server := wheelServer(t)
	entry := wheelEntry(server.URL, "numpy", "2.5.3")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, entry.FileName), wheelContent(entry.FileName), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "six-1.17.0-py3-none-any.whl")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := Verify(dir, []Entry{entry}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Complete() || len(status.Extra) != 1 || status.Extra[0] != "six-1.17.0-py3-none-any.whl" {
		t.Fatalf("Verify = %+v, want the wheel present and the stale one extra", status)
	}
	// A wheel whose bytes changed is corrupt even though no size says so.
	if err := os.WriteFile(filepath.Join(dir, entry.FileName), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, err := Verify(dir, []Entry{entry}, nil); err != nil || len(status.Corrupt) != 1 {
		t.Fatalf("Verify = %+v, %v; want the tampered wheel reported corrupt", status, err)
	}
	pruned, err := Prune(dir, []Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned) != 1 || pruned[0] != "six-1.17.0-py3-none-any.whl" {
		t.Errorf("Prune = %v, want only the wheel the lock does not name", pruned)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the stale wheel is still there")
	}
}

func TestStageFiles(t *testing.T) {
	server := wheelServer(t)
	entries := []Entry{wheelEntry(server.URL, "numpy", "2.5.3"), wheelEntry(server.URL, "six", "1.17.0")}
	poolDir, destination := t.TempDir(), filepath.Join(t.TempDir(), "frostroot-wheels")
	for _, entry := range entries {
		if err := os.WriteFile(filepath.Join(poolDir, entry.FileName), wheelContent(entry.FileName), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := StageFiles(poolDir, entries, destination, nil); err != nil {
		t.Fatal(err)
	}
	staged, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != len(entries) {
		t.Fatalf("staged %d files, want %d", len(staged), len(entries))
	}
	for _, entry := range entries {
		info, err := os.Stat(filepath.Join(destination, entry.FileName))
		if err != nil {
			t.Fatal(err)
		}
		// pip inside mmdebstrap's user namespace is "other" to these files.
		if info.Mode().Perm() != 0o644 {
			t.Errorf("%s is mode %v, want 0644", entry.FileName, info.Mode().Perm())
		}
	}
	// Staging again over an existing directory replaces what is there.
	if err := StageFiles(poolDir, entries, destination, nil); err != nil {
		t.Fatal(err)
	}
}
