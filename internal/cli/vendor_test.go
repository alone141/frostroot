package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frostroot/internal/builder"
	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// vendorFixture is a lock with two packages and a server holding their files.
type vendorFixture struct {
	recipeDir string
	lock      recipe.Lockfile
	server    *httptest.Server
	files     map[string][]byte // URL path to content; read only once the server runs
	requests  atomic.Int64
}

// newVendorFixture writes a lock with checksums into a new recipe directory
// and starts a server for it. Files are served under the pool paths the lock
// names; a nil entry in files answers 404.
func newVendorFixture(t *testing.T) *vendorFixture {
	t.Helper()
	fixture := &vendorFixture{recipeDir: t.TempDir(), files: map[string][]byte{}}
	fixture.lock = recipe.Lockfile{Version: 1, Distro: "ubuntu", Release: "24.04", Suite: "noble", Arch: "amd64", FrostrootVersion: "0.4.0", Requested: []string{"curl"}}
	for _, name := range []string{"curl", "libc6"} {
		content := []byte("deb " + name + strings.Repeat(".", 50))
		digest := sha256.Sum256(content)
		urlPath := "pool/main/" + name[:1] + "/" + name + "/" + name + "_1_amd64.deb"
		fixture.files[urlPath] = content
		fixture.lock.Packages = append(fixture.lock.Packages, recipe.LockPackage{Name: name, Version: "1", Arch: "amd64", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(content)), Filename: urlPath})
	}
	// TLS, because a lock's wheel URL must be https: the whole address
	// comes from the lock, and the pool has no base URL to fall back on.
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.requests.Add(1)
		content, found := fixture.files[strings.TrimPrefix(request.URL.Path, "/")]
		if !found {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(content) // a client that went away is not the test's concern
	}))
	t.Cleanup(fixture.server.Close)
	fixture.lock.Mirror = fixture.server.URL
	fixture.lock.Sources = []string{"deb " + fixture.server.URL + " noble main universe"}
	fixture.saveLock(t)
	return fixture
}

func (f *vendorFixture) saveLock(t *testing.T) {
	t.Helper()
	if err := recipe.SaveLock(filepath.Join(f.recipeDir, "frostroot.lock"), f.lock); err != nil {
		t.Fatal(err)
	}
}

// poolDir is where the fixture's vendor command puts files.
func (f *vendorFixture) poolDir() string { return filepath.Join(f.recipeDir, "vendor", "debs") }

// wheelPoolDir is where the fixture's vendor command puts wheels.
func (f *vendorFixture) wheelPoolDir() string { return filepath.Join(f.recipeDir, "vendor", "wheels") }

// addWheels gives the fixture's lock a Python side: wheels served by the same
// server, with no size, as pip's installation report leaves them.
func (f *vendorFixture) addWheels(t *testing.T, names ...string) {
	t.Helper()
	f.lock.Python = &recipe.LockPython{Requested: names[:1], Venv: "/opt/frostroot/venv", Interpreter: "3.12.3", PipVersion: "24.3.1"}
	for _, name := range names {
		content := []byte("wheel " + name + strings.Repeat("!", 80))
		digest := sha256.Sum256(content)
		fileName := name + "-1.0-py3-none-any.whl"
		f.files["wheels/"+fileName] = content
		f.lock.PyPI = append(f.lock.PyPI, recipe.LockPyPI{
			Name: name, Version: "1.0", SHA256: hex.EncodeToString(digest[:]),
			Filename: fileName, URL: f.server.URL + "/wheels/" + fileName,
		})
	}
	f.saveLock(t)
}

func TestVendorFillsBothPools(t *testing.T) {
	fixture := newVendorFixture(t)
	fixture.addWheels(t, "numpy", "six")
	var stdout, stderr bytes.Buffer
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	for _, wantText := range []string{"Vendored 2 packages (", "into vendor/debs: 2 downloaded.", "Vendored 2 wheels into vendor/wheels: 2 downloaded."} {
		if !strings.Contains(stdout.String(), wantText) {
			t.Errorf("stdout lacks %q:\n%s", wantText, stdout.String())
		}
	}
	// pip's report gives no size, so the lock knows none: a byte figure here
	// would be the pinned pip's alone, which is not the pool's size.
	if strings.Contains(stdout.String(), "wheels (") {
		t.Errorf("the wheel line claims a size the lock does not know:\n%s", stdout.String())
	}
	for _, name := range []string{"numpy-1.0-py3-none-any.whl", "six-1.0-py3-none-any.whl"} {
		if _, err := os.Stat(filepath.Join(fixture.wheelPoolDir(), name)); err != nil {
			t.Errorf("%s was not vendored: %v", name, err)
		}
	}
}

func TestVendorNotesAGoModuleBesideTheRecipe(t *testing.T) {
	// vendor/ is what go build reads as a module's vendored dependencies, so
	// after this command go build stops with "inconsistent vendoring" and
	// names go.mod rather than the recipe. vendor put the directory there, so
	// vendor is what says so.
	fixture := newVendorFixture(t)
	var stdout, stderr bytes.Buffer
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if strings.Contains(stdout.String(), "-mod=mod") {
		t.Errorf("a recipe directory that is no Go module was given the note:\n%s", stdout.String())
	}

	if err := os.WriteFile(filepath.Join(fixture.recipeDir, "go.mod"), []byte("module example\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "go build -mod=mod") {
		t.Errorf("stdout lacks the Go module note:\n%s", stdout.String())
	}
}

func TestVendorPrunesAWheelPoolTheLockNoLongerNames(t *testing.T) {
	// Drop [python] from a recipe and every wheel on disk is a file the lock
	// does not name; --prune promises to remove exactly those.
	fixture := newVendorFixture(t)
	if err := os.MkdirAll(fixture.wheelPoolDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(fixture.wheelPoolDir(), "numpy-2.5.3-py3-none-any.whl")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor", "--prune"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the stale wheel survived --prune: %v", err)
	}
	if !strings.Contains(stdout.String(), "numpy-2.5.3-py3-none-any.whl") {
		t.Errorf("stdout does not say what was removed:\n%s", stdout.String())
	}
}

// newVendorApp returns an App running vendor in the fixture's directory
// without a terminal and without the Launchpad fallback.
func newVendorApp(fixture *vendorFixture, stdout, stderr *bytes.Buffer) *App {
	return &App{
		Stdout:         stdout,
		Stderr:         stderr,
		RecipeDir:      fixture.recipeDir,
		GOOS:           "linux",
		VendorFallback: func(pool.Entry) string { return "" },
		VendorClient:   fixture.server.Client(),
	}
}

func TestVendorDownloadsAndResumes(t *testing.T) {
	fixture := newVendorFixture(t)
	var stdout, stderr bytes.Buffer
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	for _, wantText := range []string{"Vendored 2 packages", "into vendor/debs: 2 downloaded.", "frostroot build --offline"} {
		if !strings.Contains(stdout.String(), wantText) {
			t.Errorf("stdout lacks %q:\n%s", wantText, stdout.String())
		}
	}
	for _, wantLine := range []string{"frostroot: vendoring 2 packages · ", "frostroot: Read frostroot.lock\n", "frostroot: Check vendor/debs\n", "frostroot: Download packages\n", "frostroot:   100%  "} {
		if !strings.Contains(stderr.String(), wantLine) {
			t.Errorf("stderr lacks %q:\n%s", wantLine, stderr.String())
		}
	}
	for _, locked := range fixture.lock.Packages {
		if _, err := os.Stat(filepath.Join(fixture.poolDir(), filepath.Base(locked.Filename))); err != nil {
			t.Error(err)
		}
	}
	if fixture.requests.Load() != 2 {
		t.Errorf("requests = %d, want one per package", fixture.requests.Load())
	}

	// A second run finds everything in place and downloads nothing.
	stdout.Reset()
	stderr.Reset()
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor"}); exitCode != exitSuccess {
		t.Fatalf("second run: exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "0 downloaded, 2 already there") || fixture.requests.Load() != 2 {
		t.Errorf("second run should download nothing: requests %d, stdout:\n%s", fixture.requests.Load(), stdout.String())
	}
}

func TestVendorPruneAndExtraFiles(t *testing.T) {
	fixture := newVendorFixture(t)
	if err := os.MkdirAll(fixture.poolDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.poolDir(), "old_0_amd64.deb"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1 file(s) in vendor/debs are not in the lock; remove them with: frostroot vendor --prune") {
		t.Errorf("stdout should mention the stale file:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.poolDir(), "old_0_amd64.deb")); err != nil {
		t.Error("without --prune the stale file stays")
	}
	stdout.Reset()
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor", "--prune"}); exitCode != exitSuccess {
		t.Fatalf("--prune: exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Removed 1 file(s) the lock does not name: old_0_amd64.deb") {
		t.Errorf("stdout:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.poolDir(), "old_0_amd64.deb")); err == nil {
		t.Error("--prune must remove the stale file")
	}
}

func TestVendorRefusals(t *testing.T) {
	testCases := []struct {
		name         string
		prepare      func(t *testing.T, fixture *vendorFixture)
		args         []string
		wantInStderr string
	}{
		{
			name: "no lock",
			prepare: func(t *testing.T, fixture *vendorFixture) {
				t.Helper()
				if err := os.Remove(filepath.Join(fixture.recipeDir, "frostroot.lock")); err != nil {
					t.Fatal(err)
				}
			},
			args:         []string{"vendor"},
			wantInStderr: "no frostroot.lock",
		},
		{
			name: "lock without checksums",
			prepare: func(t *testing.T, fixture *vendorFixture) {
				t.Helper()
				for index := range fixture.lock.Packages {
					fixture.lock.Packages[index].SHA256, fixture.lock.Packages[index].Size, fixture.lock.Packages[index].Filename = "", 0, ""
				}
				fixture.lock.FrostrootVersion = "0.3.0"
				fixture.saveLock(t)
			},
			args:         []string{"vendor"},
			wantInStderr: "written by frostroot 0.3.0 and records no checksums",
		},
		{
			name:         "bad mirror",
			prepare:      func(*testing.T, *vendorFixture) {},
			args:         []string{"vendor", "--mirror", "ftp://x"},
			wantInStderr: "--mirror",
		},
		{
			name:         "unexpected argument",
			prepare:      func(*testing.T, *vendorFixture) {},
			args:         []string{"vendor", "extra"},
			wantInStderr: "unexpected argument",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newVendorFixture(t)
			testCase.prepare(t, fixture)
			var stdout, stderr bytes.Buffer
			if exitCode := newVendorApp(fixture, &stdout, &stderr).Run(testCase.args); exitCode != exitUserError {
				t.Fatalf("exit code = %d, want %d; stderr %s", exitCode, exitUserError, stderr.String())
			}
			if !strings.Contains(stderr.String(), testCase.wantInStderr) {
				t.Errorf("stderr lacks %q:\n%s", testCase.wantInStderr, stderr.String())
			}
			if fixture.requests.Load() != 0 {
				t.Error("a refused vendor must not download")
			}
		})
	}
}

func TestVendorMissingPackageIsADownloadFailure(t *testing.T) {
	fixture := newVendorFixture(t)
	delete(fixture.files, fixture.lock.Packages[1].Filename)
	var stdout, stderr bytes.Buffer
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor"}); exitCode != exitBuildFailed {
		t.Fatalf("exit code = %d, want %d; stderr %s", exitCode, exitBuildFailed, stderr.String())
	}
	for _, wantText := range []string{"libc6 1", "not found", "drops superseded packages"} {
		if !strings.Contains(stderr.String(), wantText) {
			t.Errorf("stderr lacks %q:\n%s", wantText, stderr.String())
		}
	}
}

func TestVendorMirrorFlagOverridesTheLock(t *testing.T) {
	fixture := newVendorFixture(t)
	fixture.lock.Mirror = "http://gone.invalid/ubuntu"
	fixture.saveLock(t)
	var stdout, stderr bytes.Buffer
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor", "--mirror", fixture.server.URL}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if fixture.requests.Load() != 2 {
		t.Errorf("requests = %d, want the files fetched from the --mirror server", fixture.requests.Load())
	}
}

func TestVersion(t *testing.T) {
	version := "frostroot " + builder.Version
	testCases := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{name: "no build info", info: nil, want: version + "\n"},
		{name: "without version control", info: &debug.BuildInfo{}, want: version + "\n"},
		{
			name: "from a clean checkout",
			info: &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "5325294abcdef0123456789"}, {Key: "vcs.time", Value: "2026-09-17T08:12:00Z"}, {Key: "vcs.modified", Value: "false"}}},
			want: version + " (commit 5325294, 2026-09-17)\n",
		},
		{
			name: "from a dirty checkout",
			info: &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "5325294abcdef0123456789"}, {Key: "vcs.modified", Value: "true"}}},
			want: version + " (commit 5325294+dirty)\n",
		},
	}
	for _, testCase := range testCases {
		for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
			t.Run(testCase.name+" "+args[0], func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				app := &App{Stdout: &stdout, Stderr: &stderr, RecipeDir: t.TempDir(), BuildInfo: func() (*debug.BuildInfo, bool) { return testCase.info, testCase.info != nil }}
				if exitCode := app.Run(args); exitCode != exitSuccess {
					t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
				}
				if stdout.String() != testCase.want {
					t.Errorf("stdout = %q, want %q", stdout.String(), testCase.want)
				}
			})
		}
	}
}

// writeLockCertificate saves the server's own certificate authority beside
// the recipe and records it in the lock, the way a build does for a recipe
// with [certificates].
func writeLockCertificate(t *testing.T, fixture *vendorFixture) {
	t.Helper()
	dir := filepath.Join(fixture.recipeDir, "certs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	authority := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.server.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dir, "corp.pem"), authority, 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.lock.Certificates = []recipe.LockCertificate{{Name: "corp", Path: "certs/corp.pem", SHA256: "unused-here"}}
	fixture.saveLock(t)
}

// TestVendorTrustsTheRecipeCertificates: a build fetches with the recipe's
// [certificates] as well as --ca-bundle, so a recipe whose only authority
// for an inspecting proxy lives there must vendor without the flag too.
// Otherwise build records https URLs that vendor then refuses.
func TestVendorTrustsTheRecipeCertificates(t *testing.T) {
	fixture := newVendorFixture(t)
	writeLockCertificate(t, fixture)
	var stdout, stderr bytes.Buffer
	app := newVendorApp(fixture, &stdout, &stderr)
	app.VendorClient = nil // the real trust path, not the server's own client
	if exitCode := app.Run([]string{"vendor"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	for _, entry := range fixture.lock.Packages {
		if _, err := os.Stat(filepath.Join(fixture.recipeDir, "vendor", "debs", filepath.Base(entry.Filename))); err != nil {
			t.Errorf("%s was not vendored: %v", entry.Name, err)
		}
	}
}

// The other half: with no authority for it anywhere, the same fetch must
// still refuse the private certificate rather than trust it.
func TestVendorWithoutAnyAuthorityRefusesThePrivateCertificate(t *testing.T) {
	fixture := newVendorFixture(t)
	var stdout, stderr bytes.Buffer
	app := newVendorApp(fixture, &stdout, &stderr)
	app.VendorClient = nil
	if exitCode := app.Run([]string{"vendor"}); exitCode == exitSuccess {
		t.Fatal("vendor trusted a certificate no authority signed")
	}
	if !strings.Contains(stderr.String(), "certificate") && !strings.Contains(stderr.String(), "x509") {
		t.Errorf("stderr does not say the certificate is the problem:\n%s", stderr.String())
	}
}

// A certificate the lock names but the recipe directory no longer holds is
// reported before anything is downloaded: vendoring against a different set
// of authorities than the build used should not happen quietly.
func TestVendorReportsAMissingLockCertificate(t *testing.T) {
	fixture := newVendorFixture(t)
	writeLockCertificate(t, fixture)
	if err := os.Remove(filepath.Join(fixture.recipeDir, "certs", "corp.pem")); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor"}); exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
	if !strings.Contains(stderr.String(), "certs/corp.pem") {
		t.Errorf("stderr does not name the missing certificate:\n%s", stderr.String())
	}
	if fixture.requests.Load() != 0 {
		t.Error("a refused vendor must not download")
	}
}

// addPackages gives the fixture's lock count more packages, each served by
// the same server, so that a run produces more progress events than the
// screen's buffer holds.
func (f *vendorFixture) addPackages(t *testing.T, count int) {
	t.Helper()
	for index := range count {
		name := fmt.Sprintf("pkg%03d", index)
		content := []byte("deb " + name + strings.Repeat(".", 50))
		digest := sha256.Sum256(content)
		urlPath := "pool/main/p/" + name + "/" + name + "_1_amd64.deb"
		f.files[urlPath] = content
		f.lock.Packages = append(f.lock.Packages, recipe.LockPackage{Name: name, Version: "1", Arch: "amd64", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(content)), Filename: urlPath})
	}
	f.saveLock(t)
}

// TestVendorGoesOnWhenTheScreenFails: the same fallback as build's, which
// had no test at all; with a lock of a few hundred packages the downloads
// report more events than the buffer holds.
func TestVendorGoesOnWhenTheScreenFails(t *testing.T) {
	fixture := newVendorFixture(t)
	fixture.addPackages(t, 300)
	var stdout, stderr bytes.Buffer
	app := newVendorApp(fixture, &stdout, &stderr)
	app.Stdin = failingReader{}
	app.IsTerminal = terminalChecker(true)
	app.Getenv = func(name string) string {
		if name == "TERM" {
			return "xterm-256color"
		}
		return ""
	}

	exitCode := make(chan int, 1)
	go func() { exitCode <- app.Run([]string{"vendor"}) }()
	select {
	case code := <-exitCode:
		if code != exitSuccess {
			t.Fatalf("exit code = %d, stderr %s", code, stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("vendor did not return within 30 s after its screen failed: the fallback hung")
	}
	if !strings.Contains(stderr.String(), "waiting for the downloads without it") {
		t.Fatalf("the screen did not fail, so the fallback was never exercised:\n%s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Vendored 302 packages") {
		t.Errorf("stdout lacks the summary:\n%s", stdout.String())
	}
}

// TestVendorPruneRemovesADownloadCutShort: a vendor run abandoned at the
// second Ctrl-C, or killed, exits before the download it was in the middle
// of removes its temporary file. The message on that path promises the rest
// is removed, so --prune has to be able to do it: the file counts among what
// the lock does not name, and is reported and removed like a stale package.
func TestVendorPruneRemovesADownloadCutShort(t *testing.T) {
	fixture := newVendorFixture(t)
	if err := os.MkdirAll(fixture.poolDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	cutShort := filepath.Join(fixture.poolDir(), ".curl_1_amd64.deb.4242.tmp")
	if err := os.WriteFile(cutShort, []byte("half a"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1 file(s) in vendor/debs are not in the lock; remove them with: frostroot vendor --prune") {
		t.Errorf("stdout should count the download cut short among the files the lock does not name:\n%s", stdout.String())
	}
	if _, err := os.Stat(cutShort); err != nil {
		t.Error("without --prune the file stays")
	}
	stdout.Reset()
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor", "--prune"}); exitCode != exitSuccess {
		t.Fatalf("--prune: exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Removed 1 file(s) the lock does not name: .curl_1_amd64.deb.4242.tmp") {
		t.Errorf("stdout:\n%s", stdout.String())
	}
	if _, err := os.Stat(cutShort); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("--prune must remove the download cut short, Stat error = %v", err)
	}
}
