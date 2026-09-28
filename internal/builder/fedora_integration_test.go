//go:build integration

package builder

import (
	"archive/tar"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// skipUnlessFedoraToolsAvailable skips the test on hosts that cannot build a
// Fedora image: what Preflight checks, which names what is missing.
func skipUnlessFedoraToolsAvailable(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux")
	}
	if err := (&Mkosi{}).Preflight(context.Background(), FedoraSpec{}); err != nil {
		t.Skipf("Fedora's build tools are not available: %v", err)
	}
}

// newFedoraWorkRoot returns a work root mkosi's user namespace can enter,
// and removes it when the test ends. Not t.TempDir: its parent is 0700.
func newFedoraWorkRoot(t *testing.T) string {
	t.Helper()
	cacheHome, err := os.MkdirTemp("/var/tmp", "frostroot-integration-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cacheHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeFedoraWorkRoot(t, cacheHome) })
	return cacheHome
}

// removeFedoraWorkRoot removes a test's work root. The package cache an
// unprivileged mkosi fills belongs to the subordinate ids, so what they own
// is removed first in a user namespace that maps them to its root, which
// cannot remove their top directories from the user's; then the rest, as
// the user.
func removeFedoraWorkRoot(t *testing.T, dir string) {
	t.Helper()
	if os.Getuid() != 0 {
		// Its failure is expected, on those top directories, and the
		// second step says whether anything is really left.
		output, err := exec.Command("unshare", "--map-auto", "--setuid", "0", "rm", "-rf", "--", dir).CombinedOutput()
		t.Logf("removing what the subordinate ids own in %s: %v %s", dir, err, output)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Errorf("removing %s: %v", dir, err)
	}
}

// fedoraTinyRecipe is a Fedora image of the lean base and git, whose perl
// and git-core bring hardlinks, in a locale that needs a langpack.
func fedoraTinyRecipe(t *testing.T) recipe.Recipe {
	t.Helper()
	imageRecipe := recipe.Recipe{
		Image:    recipe.Image{Name: "tiny", Distro: "fedora", Release: "44", Arch: "amd64"},
		User:     recipe.User{Name: "student", Sudo: true},
		WSL:      recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale:   recipe.Locale{Lang: "tr_TR.UTF-8", Timezone: "Asia/Tokyo"},
		Packages: recipe.Packages{Include: []string{"git"}},
	}
	if problems := recipe.Validate(imageRecipe); len(problems) != 0 {
		t.Fatal(problems)
	}
	return imageRecipe
}

// TestIntegrationFedoraTiny builds a real Fedora 44 image with mkosi and a
// Fedora tools tree, and inspects the tarball and the lock: the structure
// checks of the Ubuntu tiny image, and what a Fedora build must leave out.
//
// Needs Linux, mkosi 20.2, dnf, rpm, createrepo_c, bubblewrap and gzip, the
// network, and root or newuidmap and newgidmap with a subordinate range.
// Takes a few minutes and about 250 MB of downloads.
func TestIntegrationFedoraTiny(t *testing.T) {
	skipUnlessFedoraToolsAvailable(t)
	cacheHome := newFedoraWorkRoot(t)
	builder := Builder{Fedora: &Mkosi{}}
	progress := &testLogProgress{t: t}
	result, err := builder.Build(context.Background(), fedoraTinyRecipe(t), Options{
		RecipeDir: t.TempDir(),
		GOOS:      "linux",
		Getenv:    fakeEnvironment(map[string]string{"XDG_CACHE_HOME": cacheHome}),
		Progress:  progress,
		// An authority to trust while building and never to ship: dnf
		// verifies with it from the package manager tree, which is not the
		// image's.
		ExtraTrustPEM: certificatePEM(t, "Build-only Proxy CA"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(progress.started, FedoraPhases(false)) {
		t.Errorf("phases started = %v, want %v", progress.started, FedoraPhases(false))
	}
	if result.WorkDir != "" {
		t.Errorf("the work directory should be removed after success: %+v", result)
	}
	// The package cache stays, so that the next build downloads nothing
	// it already has; nothing else does.
	if leftovers, _ := os.ReadDir(filepath.Join(cacheHome, "frostroot")); len(leftovers) != 1 || leftovers[0].Name() != fedoraPackageCacheName {
		t.Errorf("the work root holds %v, want the package cache alone", leftovers)
	}

	lock, err := recipe.LoadLock(result.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("lock", func(t *testing.T) {
		if lock.Distro != "fedora" || lock.Release != "44" || lock.Suite != "" || lock.Mirror != "" || len(lock.Sources) != 0 || len(lock.Repositories) != 2 {
			t.Errorf("lock head = %+v", lock)
		}
		if len(lock.Packages) < 150 || len(lock.Tools) < 80 {
			t.Errorf("the lock lists %d packages and %d of the tools tree; a real build has hundreds", len(lock.Packages), len(lock.Tools))
		}
		reasons := map[string]string{}
		for _, locked := range slices.Concat(lock.Packages, lock.Tools) {
			if locked.Version == "" || locked.Arch == "" || len(locked.SHA256) != 64 || locked.Size <= 0 || locked.SourceRPM == "" ||
				!strings.HasPrefix(locked.Filename, "Packages/") || (locked.Source != "fedora" && locked.Source != "updates") {
				t.Errorf("incomplete lock entry %+v", locked)
			}
		}
		for _, locked := range lock.Packages {
			reasons[locked.Name] = locked.Reason
		}
		for name, want := range map[string]string{"git": "User", "glibc-langpack-tr": "User", "dnf5": "User", "git-core": "Dependency"} {
			if reasons[name] != want {
				t.Errorf("%s is locked with reason %q, want %q", name, reasons[name], want)
			}
		}
	})

	image := readTarball(t, result.TarballPath)
	t.Run("links", func(t *testing.T) {
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
		if symlinkCount < 1000 || hardlinkCount < 100 {
			t.Errorf("found %d symlinks and %d hardlinks; this image has thousands of each", symlinkCount, hardlinkCount)
		}
		if bin := image.entries["bin"]; bin == nil || bin.Typeflag != tar.TypeSymlink || bin.Linkname != "usr/bin" {
			t.Errorf("/bin should link to usr/bin (merged /usr): %+v", bin)
		}
	})

	t.Run("ownership, modes and capabilities", func(t *testing.T) {
		for _, entry := range image.entries {
			if entry.Uid > 65535 || entry.Gid > 65535 {
				t.Errorf("%q has a subordinate-range owner %d:%d", entry.Name, entry.Uid, entry.Gid)
			}
		}
		for _, name := range []string{"usr/bin/newuidmap", "usr/bin/newgidmap", "usr/bin/arping"} {
			if entry := image.entries[name]; entry == nil || entry.PAXRecords["SCHILY.xattr.security.capability"] == "" {
				t.Errorf("%s lost its security.capability attribute: %+v", name, entry)
			}
		}
		if sudo := image.entries["usr/bin/sudo"]; sudo == nil || sudo.Uid != 0 || sudo.Mode&0o4000 == 0 {
			t.Errorf("usr/bin/sudo must be setuid root: %+v", sudo)
		}
		if journal := image.entries["var/log/journal"]; journal == nil || journal.Gid != 190 || journal.Mode&0o2000 == 0 {
			t.Errorf("var/log/journal must be setgid systemd-journal: %+v", journal)
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
		if group := image.fileContent(t, "etc/group"); !strings.Contains(group, "\nwheel:x:10:student\n") {
			t.Error("student is not in wheel")
		}
		if sudoers := image.entries["etc/sudoers.d/90-frostroot"]; sudoers == nil || sudoers.Uid != 0 || sudoers.Mode&0o777 != 0o440 {
			t.Errorf("the sudoers drop-in must be root-owned with mode 0440: %+v", sudoers)
		}
		if localtime := image.entries["etc/localtime"]; localtime == nil || localtime.Linkname != "../usr/share/zoneinfo/Asia/Tokyo" {
			t.Errorf("etc/localtime = %+v, want a link to Asia/Tokyo", localtime)
		}
		if localeConf := image.fileContent(t, "etc/locale.conf"); localeConf != "LANG=tr_TR.UTF-8\n" {
			t.Errorf("etc/locale.conf holds %q", localeConf)
		}
		if langpack := image.entries["usr/lib/locale/tr_TR.utf8"]; langpack == nil || langpack.Typeflag != tar.TypeDir {
			t.Error("the image has no tr_TR.UTF-8 locale")
		}
		// systemd's own first-boot marker: every import makes its own id.
		if machineID := image.fileContent(t, "etc/machine-id"); machineID != "" && machineID != "uninitialized\n" {
			t.Errorf("etc/machine-id holds %q, an id every import would share", machineID)
		}
	})

	t.Run("package state", func(t *testing.T) {
		if rpmdb := image.entries["usr/lib/sysimage/rpm/rpmdb.sqlite"]; rpmdb == nil || rpmdb.Size == 0 {
			t.Errorf("the image has no rpm database: %+v", rpmdb)
		}
		if reasons := image.fileContent(t, "usr/lib/sysimage/libdnf5/packages.toml"); !strings.Contains(reasons, "\"git.x86_64\" = {reason = \"User\"}\n") {
			t.Errorf("dnf5 does not know git as the user's:\n%s", reasons)
		}
	})

	t.Run("no build leaks", func(t *testing.T) {
		for _, leftover := range []string{
			"etc/resolv.conf", "var/log/dnf5.log", "var/cache/ldconfig/aux-cache",
			"usr/lib/sysimage/libdnf5/transaction_history.sqlite", "usr/lib/sysimage/libdnf5/system-repo.lock",
			"var/lib/libdnf5/system-repo.lock", "usr/lib/sysimage/rpm/rpmdb.sqlite-shm", "usr/lib/sysimage/rpm/rpmdb.sqlite-wal",
			"etc/yum.repos.d/mkosi.repo", fedoraKeysDir, fedoraTrustDir,
		} {
			if entry := image.entries[leftover]; entry != nil {
				t.Errorf("%s must not be in the image: %+v", leftover, entry)
			}
		}
		for name, text := range image.smallFileText {
			if strings.Contains(text, cacheHome) {
				t.Errorf("%s names the build's work root %s", name, cacheHome)
			}
		}
		frozenAt := time.Unix(lock.SourceDateEpoch, 0)
		for name, header := range image.entries {
			if header.ModTime.After(frozenAt) {
				t.Errorf("%s is dated %v, after the instant the image is frozen at (%v)", name, header.ModTime, frozenAt)
			}
		}
	})
}

// TestIntegrationFedoraOfflineRebuild builds a real Fedora image online,
// vendors its packages and its tools tree's from Fedora's server, rebuilds
// it from those files alone, and checks that the rebuild has exactly the
// lock's packages and dnf5's reasons, and that a second rebuild is byte
// for byte the first.
//
// Needs what TestIntegrationFedoraTiny needs, and downloads the packages
// twice, once through mkosi and once into vendor/rpms.
func TestIntegrationFedoraOfflineRebuild(t *testing.T) {
	skipUnlessFedoraToolsAvailable(t)
	cacheHome := newFedoraWorkRoot(t)
	recipeDir := t.TempDir()
	imageRecipe := fedoraTinyRecipe(t)
	imageRecipe.Packages.Include = []string{"jq"}
	options := Options{RecipeDir: recipeDir, GOOS: "linux", Getenv: fakeEnvironment(map[string]string{"XDG_CACHE_HOME": cacheHome})}
	builder := Builder{Fedora: &Mkosi{}}

	t.Log("online build")
	online, err := builder.Build(context.Background(), imageRecipe, options)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := recipe.LoadLock(online.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := pool.Manifest(lock)
	if err != nil {
		t.Fatal(err)
	}

	t.Log("vendor")
	poolDir := filepath.Join(recipeDir, filepath.FromSlash(pool.RPMsDirName))
	summary, err := pool.Fetch(context.Background(), pool.FetchOptions{Dir: poolDir, Entries: entries, Fallback: pool.FallbackURL(lock)})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Fetched != len(entries) {
		t.Errorf("fetched %d of %d", summary.Fetched, len(entries))
	}

	t.Log("offline build")
	if err := os.Remove(online.TarballPath); err != nil {
		t.Fatal(err)
	}
	lockBefore, err := os.ReadFile(online.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	options.Offline = true
	progress := &testLogProgress{t: t}
	options.Progress = progress
	imageRecipe.User.Name = "teacher" // provisioning may change; packages may not
	imageRecipe.WSL.DefaultUser = "teacher"
	offline, err := builder.Build(context.Background(), imageRecipe, options)
	if err != nil {
		t.Fatal(err)
	}
	if !offline.Offline || !offline.Reproducible || offline.InstalledPackageCount != len(lock.Packages) || offline.SourceDateEpoch != lock.SourceDateEpoch {
		t.Errorf("Result = %+v, want a reproducible offline build of %d packages frozen at %d", offline, len(lock.Packages), lock.SourceDateEpoch)
	}
	if lockAfter, err := os.ReadFile(online.LockPath); err != nil || string(lockAfter) != string(lockBefore) {
		t.Errorf("the offline build rewrote the lock: %v", err)
	}
	if !slices.Equal(progress.started, FedoraPhases(true)) {
		t.Errorf("phases started = %v, want %v", progress.started, FedoraPhases(true))
	}
	// Its own package cache went with its tools tree.
	if leftovers, _ := os.ReadDir(filepath.Join(cacheHome, "frostroot")); len(leftovers) != 1 || leftovers[0].Name() != fedoraPackageCacheName {
		t.Errorf("the work root holds %v, want the online build's package cache alone", leftovers)
	}
	image := readTarball(t, offline.TarballPath)
	if !strings.Contains(image.fileContent(t, "etc/passwd"), "teacher:x:1000:1000") {
		t.Error("the offline build did not provision the changed user")
	}
	if reasons := image.fileContent(t, "usr/lib/sysimage/libdnf5/packages.toml"); reasons != renderFedoraReasons(lock.Packages) {
		t.Errorf("dnf5's reasons in the image are not the lock's:\n%s", reasons)
	}
	for _, leftover := range []string{"var/log/dnf5.log", "etc/resolv.conf"} {
		if entry := image.entries[leftover]; entry != nil {
			t.Errorf("%s must not be in the image: %+v", leftover, entry)
		}
	}

	t.Log("second offline build")
	firstTarball := filepath.Join(recipeDir, "first.tar.gz")
	if err := os.Rename(offline.TarballPath, firstTarball); err != nil {
		t.Fatal(err)
	}
	options.Progress = nil
	second, err := builder.Build(context.Background(), imageRecipe, options)
	if err != nil {
		t.Fatal(err)
	}
	if firstSum, secondSum := sha256Of(t, firstTarball), sha256Of(t, second.TarballPath); firstSum != secondSum {
		t.Errorf("two offline builds of one lock differ: %s and %s", firstSum, secondSum)
	} else {
		t.Logf("two offline builds of one lock: %s", firstSum)
	}
}
