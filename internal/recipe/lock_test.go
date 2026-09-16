package recipe

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sampleLock returns a small, complete lockfile.
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

// saveAndRead saves lock to a temporary file and returns the path and the
// file's content.
func saveAndRead(t *testing.T, lock Lockfile) (path, content string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "frostroot.lock")
	if err := SaveLock(path, lock); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, string(written)
}

func TestLockRoundTrip(t *testing.T) {
	original := sampleLock()
	path, _ := saveAndRead(t, original)
	reloaded, err := LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded, original) {
		t.Fatalf("round trip =\n%+v\nwant\n%+v", reloaded, original)
	}
}

func TestSaveLockIsDeterministic(t *testing.T) {
	// The same input must produce byte-identical output, so a rebuild that
	// changes nothing produces an empty git diff.
	_, firstContent := saveAndRead(t, sampleLock())
	_, secondContent := saveAndRead(t, sampleLock())
	if firstContent != secondContent {
		t.Fatalf("two saves of the same lock differ:\n%s\n---\n%s", firstContent, secondContent)
	}
}

func TestSaveLockIsDiffFriendly(t *testing.T) {
	// One source line and one package field per line, so a changed version
	// shows up as a one-line diff.
	_, content := saveAndRead(t, sampleLock())
	countLinesStartingWith := func(prefix string) int {
		count := 0
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), prefix) {
				count++
			}
		}
		return count
	}
	if got := countLinesStartingWith("'deb ") + countLinesStartingWith(`"deb `); got != 3 {
		t.Errorf("found %d source lines, want each of the 3 on its own line:\n%s", got, content)
	}
	if got := countLinesStartingWith("[[packages]]"); got != 2 {
		t.Errorf("found %d [[packages]] tables, want one per package:\n%s", got, content)
	}
	if !strings.HasPrefix(content, "version = 1\n") {
		t.Errorf("version must lead the file:\n%s", content)
	}
}

func TestSaveLockRecordsEmptyRequestedExplicitly(t *testing.T) {
	lock := sampleLock()
	lock.Requested = nil
	path, content := saveAndRead(t, lock)
	if !strings.Contains(content, "requested = []") {
		t.Fatalf("an empty include list should still be written:\n%s", content)
	}
	reloaded, err := LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Requested) != 0 {
		t.Fatalf("Requested = %q, want empty", reloaded.Requested)
	}
}

func TestLoadLockRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frostroot.lock")
	if err := os.WriteFile(path, []byte("version = 1\nsurprise = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLock(path); err == nil {
		t.Fatal("LoadLock succeeded, want an error for the unknown field")
	}
}
