package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleLock() Lockfile {
	return Lockfile{
		Version: 1,
		Distro:  "ubuntu",
		Release: "22.04",
		Suite:   "jammy",
		Arch:    "amd64",
		Mirror:  "http://archive.ubuntu.com/ubuntu",
		Sources: []string{
			"deb http://archive.ubuntu.com/ubuntu jammy main universe",
			"deb http://archive.ubuntu.com/ubuntu jammy-updates main universe",
			"deb http://archive.ubuntu.com/ubuntu jammy-security main universe",
		},
		FrostrootVersion: "0.1.0",
		Requested:        []string{"git", "build-essential", "cmake"},
		Packages: []LockPackage{
			{Name: "git", Version: "1:2.34.1-1ubuntu1.11", Arch: "amd64"},
			{Name: "libc6", Version: "2.35-0ubuntu3.8", Arch: "amd64"},
		},
	}
}

func TestLockRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frostroot.lock")
	in := sampleLock()
	if err := SaveLock(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Version != 1 || out.Distro != "ubuntu" || out.Release != "22.04" || out.Suite != "jammy" ||
		out.Arch != "amd64" || out.Mirror != in.Mirror || out.FrostrootVersion != "0.1.0" {
		t.Fatalf("header: %+v", out)
	}
	if len(out.Sources) != 3 || !strings.Contains(out.Sources[2], "jammy-security") {
		t.Fatalf("sources: %#v", out.Sources)
	}
	if len(out.Requested) != 3 || out.Requested[0] != "git" {
		t.Fatalf("requested: %#v", out.Requested)
	}
	if len(out.Packages) != 2 || out.Packages[0] != in.Packages[0] || out.Packages[1] != in.Packages[1] {
		t.Fatalf("packages: %#v", out.Packages)
	}
}

func TestSaveLockIsDeterministic(t *testing.T) {
	// Same input must produce byte-identical output, so a rebuild that
	// changes nothing produces an empty git diff.
	dir := t.TempDir()
	in := sampleLock()
	a := filepath.Join(dir, "a.lock")
	b := filepath.Join(dir, "b.lock")
	if err := SaveLock(a, in); err != nil {
		t.Fatal(err)
	}
	if err := SaveLock(b, in); err != nil {
		t.Fatal(err)
	}
	ba, _ := os.ReadFile(a)
	bb, _ := os.ReadFile(b)
	if string(ba) != string(bb) {
		t.Fatal("SaveLock is not deterministic")
	}
}

func TestSaveLockIsDiffFriendly(t *testing.T) {
	// One source line and one package field per line, so a changed version
	// shows up as a one-line diff.
	path := filepath.Join(t.TempDir(), "frostroot.lock")
	if err := SaveLock(path, sampleLock()); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	lines := strings.Split(text, "\n")
	count := func(prefix string) int {
		n := 0
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), prefix) {
				n++
			}
		}
		return n
	}
	if n := count("'deb ") + count(`"deb `); n != 3 {
		t.Fatalf("want each source on its own line, got %d:\n%s", n, text)
	}
	if n := count("[[packages]]"); n != 2 {
		t.Fatalf("want one [[packages]] table per package, got %d:\n%s", n, text)
	}
	if !strings.HasPrefix(text, "version = 1\n") {
		t.Fatalf("version must lead the file:\n%s", text)
	}
}

func TestSaveLockEmptyRequestedIsExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frostroot.lock")
	in := sampleLock()
	in.Requested = nil
	if err := SaveLock(path, in); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "requested = []") {
		t.Fatalf("an empty include list should still be recorded:\n%s", body)
	}
	out, err := LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Requested) != 0 {
		t.Fatalf("requested %#v", out.Requested)
	}
}

func TestLoadLockRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frostroot.lock")
	if err := os.WriteFile(path, []byte("version = 1\nsurprise = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLock(path); err == nil {
		t.Fatal("expected error for unknown field")
	}
}
