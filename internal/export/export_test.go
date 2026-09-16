package export

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestTarballRelPath(t *testing.T) {
	got := TarballRelPath("cpp-lab", "22.04", "amd64")
	want := filepath.Join("dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func writeSrc(t *testing.T, body string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "image.tar.gz")
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

func assertOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(names) {
		t.Fatalf("want %v in %s, got %v", names, dir, entries)
	}
	for i, e := range entries {
		if e.Name() != names[i] {
			t.Fatalf("want %v in %s, got %v", names, dir, entries)
		}
	}
}

func TestPlaceCreatesParentAndMoves(t *testing.T) {
	src := writeSrc(t, "payload")
	dest := filepath.Join(t.TempDir(), "dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")
	if err := Place(src, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload" {
		t.Fatalf("content %q", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source should be gone: %v", err)
	}
}

func TestPlaceOverwritesExisting(t *testing.T) {
	// build overwrites a matching tarball without asking.
	dest := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Place(writeSrc(t, "fresh"), dest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "fresh" {
		t.Fatalf("content %q", got)
	}
}

func TestPlaceLeavesNoTmpOnSuccess(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	if err := Place(writeSrc(t, "x"), dest); err != nil {
		t.Fatal(err)
	}
	assertOnly(t, filepath.Dir(dest), "out.tar.gz")
}

// crossDevice makes the first rename fail the way it does when the work
// directory and dist/ are on different filesystems (the normal case under
// WSL, where dist/ often sits on a 9p mount of a Windows drive).
func crossDevice(t *testing.T) {
	t.Helper()
	orig := rename
	rename = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { rename = orig })
}

func TestPlaceCopiesAcrossDevices(t *testing.T) {
	crossDevice(t)
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	src := writeSrc(t, "payload")
	dest := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Place(src, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload" {
		t.Fatalf("content %q", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source should be gone after a cross-device move: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v, want 0644 so the tarball is readable for wsl --import", info.Mode().Perm())
	}
	assertOnly(t, filepath.Dir(dest), "out.tar.gz")
}

func TestPlaceAcrossDevicesRespectsUmaskWithoutChmod(t *testing.T) {
	// dist/ is routinely on a drvfs mount of a Windows drive, where chmod
	// fails with EPERM. The copy must not depend on chmod: create the
	// temporary file with the final mode and let the umask apply, as for any
	// file a user creates.
	crossDevice(t)
	old := syscall.Umask(0o027)
	defer syscall.Umask(old)
	dest := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	if err := Place(writeSrc(t, "payload"), dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v, want 0644 minus umask 027", info.Mode().Perm())
	}
}

func TestPlaceAcrossDevicesDoesNotClobberConcurrentTemps(t *testing.T) {
	crossDevice(t)
	dir := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Another build's temporary file for the same destination.
	other := filepath.Join(dir, ".out.tar.gz.12345.tmp")
	if err := os.WriteFile(other, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Place(writeSrc(t, "ours"), filepath.Join(dir, "out.tar.gz")); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(other); string(body) != "theirs" {
		t.Fatal("another build's temporary file was touched")
	}
}

func TestPlaceMissingSourceAcrossDevices(t *testing.T) {
	crossDevice(t)
	dest := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	if err := Place(filepath.Join(t.TempDir(), "absent.tar.gz"), dest); err == nil {
		t.Fatal("expected error")
	}
	assertOnly(t, filepath.Dir(dest))
}

func TestPlaceDoesNotCopyOnOtherErrors(t *testing.T) {
	// Only EXDEV means "try copying". Anything else is a real failure and the
	// source must be left where it is.
	orig := rename
	rename = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EACCES}
	}
	t.Cleanup(func() { rename = orig })

	src := writeSrc(t, "payload")
	dest := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	err := Place(src, dest)
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("dest must not exist: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source must be kept: %v", err)
	}
}
