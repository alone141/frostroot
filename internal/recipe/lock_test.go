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

// sampleLockWithChecksums returns sampleLock with the fields a frostroot 0.4
// build records for vendoring.
func sampleLockWithChecksums() Lockfile {
	lock := sampleLock()
	lock.FrostrootVersion = "0.4.0"
	lock.Packages[0].SHA256 = "af7af42226d21bcbf87cf62a9e158bd5dfbd051ebbde8818ca441dc8085f67af"
	lock.Packages[0].Size = 4_000_000
	lock.Packages[0].Filename = "pool/main/g/git/git_1%3a2.34.1-1ubuntu1.11_amd64.deb"
	lock.Packages[1].SHA256 = "af36c7ac770770fe3d3c10e85d6bc538e76e57570ba7db7d397fb9f654783ef3"
	lock.Packages[1].Size = 3_264_806
	lock.Packages[1].Filename = "pool/main/g/glibc/libc6_2.35-0ubuntu3.8_amd64.deb"
	return lock
}

func TestLockChecksumsRoundTrip(t *testing.T) {
	original := sampleLockWithChecksums()
	path, content := saveAndRead(t, original)
	for _, wantLine := range []string{"sha256 = 'af7af42226d21bcbf87cf62a9e158bd5dfbd051ebbde8818ca441dc8085f67af'", "size = 3264806", "filename = 'pool/main/g/glibc/libc6_2.35-0ubuntu3.8_amd64.deb'"} {
		if !strings.Contains(content, wantLine) {
			t.Errorf("saved lock lacks %q:\n%s", wantLine, content)
		}
	}
	reloaded, err := LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded, original) {
		t.Fatalf("round trip =\n%+v\nwant\n%+v", reloaded, original)
	}
}

func TestLockHasChecksums(t *testing.T) {
	withoutSize := sampleLockWithChecksums()
	withoutSize.Packages[1].Size = 0
	withoutFilename := sampleLockWithChecksums()
	withoutFilename.Packages[0].Filename = ""
	testCases := []struct {
		name string
		lock Lockfile
		want bool
	}{
		{name: "frostroot 0.4 lock", lock: sampleLockWithChecksums(), want: true},
		{name: "frostroot 0.3 lock", lock: sampleLock(), want: false},
		{name: "one package without a size", lock: withoutSize, want: false},
		{name: "one package without a file name", lock: withoutFilename, want: false},
		{name: "no packages", lock: Lockfile{Version: 1}, want: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.lock.HasChecksums(); got != testCase.want {
				t.Errorf("HasChecksums() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestLoadLockAcceptsOlderLockWithoutChecksums(t *testing.T) {
	// A lock written by frostroot 0.3 has name, version and arch only. It
	// must still load, so validate and tests keep working on it; vendor is
	// what refuses it.
	path, _ := saveAndRead(t, sampleLock())
	reloaded, err := LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.HasChecksums() {
		t.Fatal("an old lock must not claim checksums")
	}
	if !reflect.DeepEqual(reloaded, sampleLock()) {
		t.Fatalf("round trip of an old lock =\n%+v", reloaded)
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
