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
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// TestIntegrationNobleTiny builds a real image with mmdebstrap and inspects
// the tarball. It is the only automated check that the image is structurally
// sound: an earlier plan passed every unit test while producing a tarball in
// which every symlink had an empty target.
//
// Needs Linux, mmdebstrap, ubuntu-keyring, network, and user namespaces or
// root. Several minutes and a few hundred MB of downloads.
//
//	go test -tags=integration -run TestIntegration -v -timeout 30m ./internal/builder/
func TestIntegrationNobleTiny(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	if _, err := exec.LookPath("mmdebstrap"); err != nil {
		t.Skip("mmdebstrap not installed")
	}
	if _, err := os.Stat(UbuntuKeyring); err != nil {
		t.Skip("ubuntu-keyring not installed")
	}

	dir := t.TempDir()
	// Not t.TempDir: its parent is 0700, and in unshare mode mmdebstrap's
	// namespace cannot enter it. That is exactly the failure this test found
	// first; Preflight now reports it (see the test below).
	cache, err := os.MkdirTemp("/var/tmp", "frostroot-integration-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(cache) })
	r := recipe.Recipe{
		Image:  recipe.Image{Name: "tiny", Release: "24.04", Arch: "amd64"},
		User:   recipe.User{Name: "student", Sudo: true},
		WSL:    recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale: recipe.Locale{Lang: "en_US.UTF-8", Timezone: "Asia/Tokyo"},
		// bash is essential anyway; the point is the essentials list.
		Packages: recipe.Packages{Include: []string{"bash"}},
	}
	if probs := recipe.Validate(r); len(probs) != 0 {
		t.Fatal(probs)
	}
	b := Builder{Bootstrap: &Mmdebstrap{Stderr: testWriter{t}}}
	res, err := b.Build(context.Background(), r, Options{
		Dir:    dir,
		GOOS:   "linux",
		Getenv: func(k string) string { return map[string]string{"XDG_CACHE_HOME": cache}[k] },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.WorkDir != "" {
		t.Fatalf("work directory should be removed on success: %+v", res)
	}
	if entries, _ := os.ReadDir(filepath.Join(cache, "frostroot")); len(entries) != 0 {
		t.Fatalf("work root not cleaned up: %v", entries)
	}

	// The lock: every installed package, exact versions, the three pockets.
	lock, err := recipe.LoadLock(res.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Sources) != 3 || !strings.HasSuffix(lock.Sources[1], "noble-updates main universe") || !strings.HasSuffix(lock.Sources[2], "noble-security main universe") {
		t.Fatalf("lock sources: %#v", lock.Sources)
	}
	if len(lock.Packages) < 150 {
		t.Fatalf("a real image has hundreds of packages, the lock has %d", len(lock.Packages))
	}
	for i, p := range lock.Packages {
		if p.Version == "" || p.Arch == "" {
			t.Fatalf("incomplete lock entry %#v", p)
		}
		if i > 0 && lock.Packages[i-1].Name > p.Name {
			t.Fatalf("lock not sorted at %s", p.Name)
		}
	}
	for _, name := range []string{"bash", "systemd", "systemd-sysv", "dbus", "sudo", "locales", "tzdata", "passwd", "ca-certificates"} {
		assertLockHas(t, lock, name)
	}

	entries := readTar(t, res.TarballPath)

	// Regression test for the defect that motivated the 2026-09-15 plan: a
	// symlink with an empty Linkname means the image cannot boot.
	symlinks, hardlinks := 0, 0
	for _, e := range entries {
		switch e.Typeflag {
		case tar.TypeSymlink:
			symlinks++
			if e.Linkname == "" {
				t.Fatalf("symlink %q has an empty target", e.Name)
			}
		case tar.TypeLink:
			hardlinks++
			if e.Linkname == "" {
				t.Fatalf("hardlink %q has an empty target", e.Name)
			}
		}
	}
	if symlinks < 1000 {
		t.Fatalf("a real rootfs has thousands of symlinks, found %d", symlinks)
	}
	if hardlinks == 0 {
		t.Fatal("hardlinks were flattened into copies (perl5.* -> perl is a hardlink)")
	}
	if e := find(entries, "bin"); e == nil || e.Typeflag != tar.TypeSymlink || e.Linkname != "usr/bin" {
		t.Fatalf("merged /usr: %+v", e)
	}

	// Ownership must be real, not remapped into the subuid range.
	for _, e := range entries {
		if e.Uid > 65535 || e.Gid > 65535 {
			t.Fatalf("entry %q has subuid-range owner %d:%d", e.Name, e.Uid, e.Gid)
		}
	}

	// File capabilities survive: ping needs cap_net_raw.
	if ping := find(entries, "usr/bin/ping"); ping == nil || ping.PAXRecords["SCHILY.xattr.security.capability"] == "" {
		t.Fatalf("usr/bin/ping lost its security.capability xattr: %+v", ping)
	}

	// Provisioning.
	wslConf := tarFileBody(t, res.TarballPath, "etc/wsl.conf")
	for _, want := range []string{"[boot]\nsystemd=true", "[user]\ndefault=student", "[time]\nuseWindowsTimezone=false"} {
		if !strings.Contains(wslConf, want) {
			t.Fatalf("wsl.conf missing %q:\n%s", want, wslConf)
		}
	}
	home := find(entries, "home/student")
	if home == nil || home.Typeflag != tar.TypeDir || home.Uid != 1000 || home.Gid != 1000 {
		t.Fatalf("home/student must be a directory owned by the user: %+v", home)
	}
	if !strings.Contains(tarFileBody(t, res.TarballPath, "etc/passwd"), "student:x:1000:1000::/home/student:/bin/bash") {
		t.Fatal("student is not in /etc/passwd with bash as the shell")
	}
	sudoers := find(entries, "etc/sudoers.d/90-frostroot")
	if sudoers == nil || sudoers.Uid != 0 || sudoers.Mode&0o777 != 0o440 {
		t.Fatalf("sudoers drop-in must be root-owned 0440: %+v", sudoers)
	}
	if body := tarFileBody(t, res.TarballPath, "etc/sudoers.d/90-frostroot"); body != "student ALL=(ALL) NOPASSWD:ALL\n" {
		t.Fatalf("sudoers: %q", body)
	}
	if lt := find(entries, "etc/localtime"); lt == nil || lt.Linkname != "/usr/share/zoneinfo/Asia/Tokyo" {
		t.Fatalf("localtime: %+v", lt)
	}
	if tz := tarFileBody(t, res.TarballPath, "etc/timezone"); tz != "Asia/Tokyo\n" {
		t.Fatalf("timezone: %q", tz)
	}
	if !strings.Contains(tarFileBody(t, res.TarballPath, "etc/locale.conf"), "LANG=en_US.UTF-8") {
		t.Fatal("default locale not set")
	}
	if la := find(entries, "usr/lib/locale/locale-archive"); la == nil || la.Size == 0 {
		t.Fatal("no compiled locales in the image")
	}

	// mmdebstrap copies the host's resolv.conf and hostname in; the provision
	// script removes them. machine-id is emptied so every import gets its own.
	assertTarLacks(t, entries, "etc/resolv.conf")
	assertTarLacks(t, entries, "etc/hostname")
	if mid := find(entries, "etc/machine-id"); mid == nil || mid.Size != 0 {
		t.Fatalf("machine-id must exist and be empty: %+v", mid)
	}
}

// TestIntegrationUnreachableWorkRootFailsFast: a work root that mmdebstrap's
// user namespace cannot enter must be reported before anything is downloaded,
// not as a wall of "Permission denied" from inside mmdebstrap.
func TestIntegrationUnreachableWorkRootFailsFast(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getuid() == 0 {
		t.Skip("unshare mode on Linux only")
	}
	if _, err := exec.LookPath("mmdebstrap"); err != nil {
		t.Skip("mmdebstrap not installed")
	}
	if _, err := os.Stat(UbuntuKeyring); err != nil {
		t.Skip("ubuntu-keyring not installed")
	}
	private := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(private, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(private, 0o750); err != nil {
		t.Fatal(err)
	}
	r := recipe.Recipe{
		Image: recipe.Image{Name: "tiny", Release: "24.04", Arch: "amd64"},
		User:  recipe.User{Name: "student"},
	}
	b := Builder{Bootstrap: &Mmdebstrap{}}
	res, err := b.Build(context.Background(), r, Options{
		Dir:    t.TempDir(),
		GOOS:   "linux",
		Getenv: func(k string) string { return map[string]string{"XDG_CACHE_HOME": private + "/.cache"}[k] },
	})
	if !errors.Is(err, ErrBadWorkRoot) {
		t.Fatalf("want ErrBadWorkRoot, got %v", err)
	}
	if res.WorkDir != "" {
		t.Fatalf("nothing should have been created: %+v", res)
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func readTar(t *testing.T, path string) []*tar.Header {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out []*tar.Header
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		h.Name = strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/")
		out = append(out, h)
	}
}

func find(entries []*tar.Header, name string) *tar.Header {
	for _, e := range entries {
		if e.Name == name {
			return e
		}
	}
	return nil
}

func assertTarLacks(t *testing.T, entries []*tar.Header, name string) {
	t.Helper()
	if e := find(entries, name); e != nil {
		t.Fatalf("%s must not be in the image: %+v", name, e)
	}
}

func tarFileBody(t *testing.T, path, name string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			t.Fatalf("%s not in tarball", name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimPrefix(h.Name, "./") == name {
			if h.Typeflag != tar.TypeReg {
				t.Fatalf("%s is not a regular file: %+v", name, h)
			}
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			return string(body)
		}
	}
}

func assertLockHas(t *testing.T, lock recipe.Lockfile, name string) {
	t.Helper()
	for _, p := range lock.Packages {
		if p.Name == name {
			return
		}
	}
	t.Fatalf("lock has no %s", name)
}
