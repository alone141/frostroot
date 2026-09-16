//go:build integration

package builder

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// skipUnlessMmdebstrapAvailable skips the test on hosts that cannot run a real
// bootstrap.
func skipUnlessMmdebstrapAvailable(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux")
	}
	if _, err := exec.LookPath("mmdebstrap"); err != nil {
		t.Skip("mmdebstrap not installed")
	}
	if _, err := os.Stat(UbuntuArchiveKeyring); err != nil {
		t.Skip("ubuntu-keyring not installed")
	}
}

// TestIntegrationNobleTiny builds a real image with mmdebstrap and inspects
// the tarball. It is the only automated check that the image is structurally
// sound: an earlier plan passed every unit test while producing a tarball in
// which every symlink had an empty target.
//
// Needs Linux, mmdebstrap, ubuntu-keyring, network, and user namespaces or
// root. Takes a few minutes and a few hundred MB of downloads:
//
//	go test -tags=integration -run TestIntegration -v -timeout 30m ./internal/builder/
func TestIntegrationNobleTiny(t *testing.T) {
	skipUnlessMmdebstrapAvailable(t)

	// Not t.TempDir: its parent is 0700, and in unshare mode mmdebstrap's user
	// namespace cannot enter it. That is exactly the failure this test found
	// first; Preflight now reports it (see the test below).
	cacheHome, err := os.MkdirTemp("/var/tmp", "frostroot-integration-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cacheHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cacheHome) })

	imageRecipe := recipe.Recipe{
		Image:  recipe.Image{Name: "tiny", Release: "24.04", Arch: "amd64"},
		User:   recipe.User{Name: "student", Sudo: true},
		WSL:    recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale: recipe.Locale{Lang: "en_US.UTF-8", Timezone: "Asia/Tokyo"},
		// bash is essential anyway; the point is the essential packages.
		Packages: recipe.Packages{Include: []string{"bash"}},
	}
	if problems := recipe.Validate(imageRecipe); len(problems) != 0 {
		t.Fatal(problems)
	}
	builder := Builder{Bootstrapper: &Mmdebstrap{}}
	progress := &testLogProgress{t: t}
	result, err := builder.Build(context.Background(), imageRecipe, Options{
		RecipeDir: t.TempDir(),
		GOOS:      "linux",
		Getenv:    fakeEnvironment(map[string]string{"XDG_CACHE_HOME": cacheHome}),
		Progress:  progress,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("progress", func(t *testing.T) {
		wantStarted := []Phase{PhaseUpdateIndex, PhaseDownload, PhaseExtract, PhaseInstallEssential, PhaseInstallRequested, PhaseProvision, PhaseCreateTarball, PhaseWriteLock, PhasePlaceTarball}
		if !slices.Equal(progress.started, wantStarted) {
			t.Errorf("phases started = %v, want %v", progress.started, wantStarted)
		}
		if progress.downloadTotalBytes <= 0 {
			t.Error("the download phase never reported a total")
		}
		if progress.installSteps <= 0 {
			t.Error("the install phases never reported a step")
		}
	})
	if result.WorkDir != "" {
		t.Errorf("the work directory should be removed after success: %+v", result)
	}
	if leftovers, _ := os.ReadDir(filepath.Join(cacheHome, "frostroot")); len(leftovers) != 0 {
		t.Errorf("work root not cleaned up: %v", leftovers)
	}

	t.Run("lock", func(t *testing.T) {
		lock, err := recipe.LoadLock(result.LockPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(lock.Sources) != 3 || !strings.HasSuffix(lock.Sources[1], "noble-updates main universe") || !strings.HasSuffix(lock.Sources[2], "noble-security main universe") {
			t.Errorf("Sources = %q, want the three pockets", lock.Sources)
		}
		if len(lock.Packages) < 150 {
			t.Errorf("the lock lists %d packages; a real image has hundreds", len(lock.Packages))
		}
		lockedNames := map[string]bool{}
		for index, locked := range lock.Packages {
			if locked.Version == "" || locked.Arch == "" {
				t.Errorf("incomplete lock entry %+v", locked)
			}
			if index > 0 && lock.Packages[index-1].Name > locked.Name {
				t.Errorf("lock not sorted at %s", locked.Name)
			}
			lockedNames[locked.Name] = true
		}
		for _, wantPackage := range append([]string{"bash"}, EssentialPackages...) {
			if !lockedNames[wantPackage] {
				t.Errorf("the lock has no %s", wantPackage)
			}
		}
	})

	image := readTarball(t, result.TarballPath)

	t.Run("links", func(t *testing.T) {
		// Regression test for the defect that motivated the 2026-09-15 plan: a
		// symlink with an empty target means the image cannot boot.
		symlinkCount, hardlinkCount := 0, 0
		for _, entry := range image.entries {
			switch entry.Typeflag {
			case tar.TypeSymlink:
				symlinkCount++
				if entry.Linkname == "" {
					t.Errorf("symlink %q has an empty target", entry.Name)
				}
			case tar.TypeLink:
				hardlinkCount++
				if entry.Linkname == "" {
					t.Errorf("hardlink %q has an empty target", entry.Name)
				}
			}
		}
		if symlinkCount < 1000 {
			t.Errorf("found %d symlinks; a real root filesystem has thousands", symlinkCount)
		}
		if hardlinkCount == 0 {
			t.Error("hardlinks were flattened into copies (perl5.* is a hardlink to perl)")
		}
		if bin := image.entries["bin"]; bin == nil || bin.Typeflag != tar.TypeSymlink || bin.Linkname != "usr/bin" {
			t.Errorf("/bin should link to usr/bin (merged /usr): %+v", bin)
		}
	})

	t.Run("ownership and capabilities", func(t *testing.T) {
		// Ownership must be real, not remapped into the subordinate uid range.
		for _, entry := range image.entries {
			if entry.Uid > 65535 || entry.Gid > 65535 {
				t.Errorf("%q has a subordinate-range owner %d:%d", entry.Name, entry.Uid, entry.Gid)
			}
		}
		// File capabilities survive: ping needs cap_net_raw.
		if ping := image.entries["usr/bin/ping"]; ping == nil || ping.PAXRecords["SCHILY.xattr.security.capability"] == "" {
			t.Errorf("usr/bin/ping lost its security.capability attribute: %+v", ping)
		}
	})

	t.Run("provisioning", func(t *testing.T) {
		wslConf := image.fileContent(t, "etc/wsl.conf")
		for _, wantSection := range []string{"[boot]\nsystemd=true", "[user]\ndefault=student", "[time]\nuseWindowsTimezone=false"} {
			if !strings.Contains(wslConf, wantSection) {
				t.Errorf("wsl.conf lacks %q:\n%s", wantSection, wslConf)
			}
		}
		if home := image.entries["home/student"]; home == nil || home.Typeflag != tar.TypeDir || home.Uid != 1000 || home.Gid != 1000 {
			t.Errorf("home/student must be a directory owned by the user: %+v", home)
		}
		if passwd := image.fileContent(t, "etc/passwd"); !strings.Contains(passwd, "student:x:1000:1000::/home/student:/bin/bash") {
			t.Error("student is not in /etc/passwd with bash as the shell")
		}
		if sudoers := image.entries["etc/sudoers.d/90-frostroot"]; sudoers == nil || sudoers.Uid != 0 || sudoers.Mode&0o777 != 0o440 {
			t.Errorf("the sudoers drop-in must be root-owned with mode 0440: %+v", sudoers)
		}
		if sudoers := image.fileContent(t, "etc/sudoers.d/90-frostroot"); sudoers != "student ALL=(ALL) NOPASSWD:ALL\n" {
			t.Errorf("sudoers drop-in holds %q", sudoers)
		}
		if localtime := image.entries["etc/localtime"]; localtime == nil || localtime.Linkname != "/usr/share/zoneinfo/Asia/Tokyo" {
			t.Errorf("etc/localtime = %+v, want a link to Asia/Tokyo", localtime)
		}
		if timezone := image.fileContent(t, "etc/timezone"); timezone != "Asia/Tokyo\n" {
			t.Errorf("etc/timezone holds %q", timezone)
		}
		if !strings.Contains(image.fileContent(t, "etc/locale.conf"), "LANG=en_US.UTF-8") {
			t.Error("the default locale is not set")
		}
		if localeArchive := image.entries["usr/lib/locale/locale-archive"]; localeArchive == nil || localeArchive.Size == 0 {
			t.Error("the image has no compiled locales")
		}
	})

	t.Run("no host files", func(t *testing.T) {
		// mmdebstrap copies the host's resolv.conf and hostname in, and the
		// provision script removes them. machine-id is emptied so that every
		// import gets its own.
		for _, hostFile := range []string{"etc/resolv.conf", "etc/hostname"} {
			if entry := image.entries[hostFile]; entry != nil {
				t.Errorf("%s must not be in the image: %+v", hostFile, entry)
			}
		}
		if machineID := image.entries["etc/machine-id"]; machineID == nil || machineID.Size != 0 {
			t.Errorf("etc/machine-id must exist and be empty: %+v", machineID)
		}
	})
}

// TestIntegrationUnreachableWorkRootFailsFast checks that a work root which
// mmdebstrap's user namespace cannot enter is reported before anything is
// downloaded, not as a wall of "Permission denied" from inside mmdebstrap.
func TestIntegrationUnreachableWorkRootFailsFast(t *testing.T) {
	skipUnlessMmdebstrapAvailable(t)
	if os.Getuid() == 0 {
		t.Skip("root mode has no user namespace")
	}
	privateHome := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(privateHome, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(privateHome, 0o750); err != nil {
		t.Fatal(err)
	}
	imageRecipe := recipe.Recipe{
		Image: recipe.Image{Name: "tiny", Release: "24.04", Arch: "amd64"},
		User:  recipe.User{Name: "student"},
	}
	builder := Builder{Bootstrapper: &Mmdebstrap{}}
	result, err := builder.Build(context.Background(), imageRecipe, Options{
		RecipeDir: t.TempDir(),
		GOOS:      "linux",
		Getenv:    fakeEnvironment(map[string]string{"XDG_CACHE_HOME": filepath.Join(privateHome, ".cache")}),
	})
	if !errors.Is(err, ErrBadWorkRoot) {
		t.Fatalf("Build error = %v, want ErrBadWorkRoot", err)
	}
	if result.WorkDir != "" {
		t.Fatalf("nothing should have been created: %+v", result)
	}
}

// testLogProgress sends mmdebstrap's output to the test log and records what
// the parser made of it.
type testLogProgress struct {
	t                  *testing.T
	started            []Phase
	downloadTotalBytes int64
	installSteps       int64
}

func (p *testLogProgress) Report(event ProgressEvent) {
	switch event.Kind {
	case EventLogLine:
		p.t.Log(event.Line)
	case EventPhaseStarted:
		p.started = append(p.started, event.Phase)
	case EventProgress:
		if event.Phase == PhaseDownload && event.Unit == UnitBytes {
			p.downloadTotalBytes = event.Total
		}
		if event.Unit == UnitSteps {
			p.installSteps = event.Done
		}
	case EventPhaseFinished:
	}
}

// maxRecordedFileBytes bounds which regular files readTarball keeps the
// content of; the configuration files the test inspects are small.
const maxRecordedFileBytes = 64 << 10

// tarballContents is an image tarball read into memory: every header, and the
// content of every small regular file. Names have no leading "./" or trailing
// "/".
type tarballContents struct {
	entries       map[string]*tar.Header
	smallFileText map[string]string
}

// readTarball reads the gzip-compressed tarball at path.
func readTarball(t *testing.T, path string) tarballContents {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }() // read-only: closing cannot lose data
	decompressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	contents := tarballContents{entries: map[string]*tar.Header{}, smallFileText: map[string]string{}}
	archive := tar.NewReader(decompressed)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return contents
		}
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(header.Name, "./"), "/")
		contents.entries[name] = header
		if header.Typeflag == tar.TypeReg && header.Size <= maxRecordedFileBytes {
			content, err := io.ReadAll(archive)
			if err != nil {
				t.Fatal(err)
			}
			contents.smallFileText[name] = string(content)
		}
	}
}

// fileContent returns the content of a small regular file in the tarball,
// failing the test if there is none.
func (c tarballContents) fileContent(t *testing.T, name string) string {
	t.Helper()
	content, found := c.smallFileText[name]
	if !found {
		t.Fatalf("%s is not a small regular file in the tarball: %+v", name, c.entries[name])
	}
	return content
}
