package deb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frostroot/internal/deb/debtest"
)

// sampleControl is a control file as dpkg-deb writes it.
const sampleControl = `Package: hello
Version: 2.10-3build1
Architecture: amd64
Maintainer: Ubuntu Developers <ubuntu-devel-discuss@lists.ubuntu.com>
Installed-Size: 280
Depends: libc6 (>= 2.34)
Section: devel
Priority: optional
Homepage: http://www.gnu.org/software/hello/
Description: example package based on GNU hello
 The GNU hello program produces a familiar, friendly greeting.  It
 allows non-programmers to use a classic computer science tool which
 would otherwise be unavailable to them.
 .
 Seriously, though: this is an example of how to do a Debian package.
`

// buildDeb writes a .deb file named fileName into dir whose control.tar uses
// the given compression extension, and returns its path.
func buildDeb(t *testing.T, dir, fileName, extension, control string) string {
	t.Helper()
	return debtest.Build(t, dir, fileName, extension, control)
}

// collectStanzas reads every stanza of text.
func collectStanzas(t *testing.T, text string) []Stanza {
	t.Helper()
	var stanzas []Stanza
	if err := ReadStanzas(strings.NewReader(text), func(stanza Stanza) error {
		stanzas = append(stanzas, stanza)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return stanzas
}

func TestReadStanzas(t *testing.T) {
	testCases := []struct {
		name string
		text string
		want []Stanza
	}{
		{
			name: "two paragraphs, the last without a blank line",
			text: "Package: a\nVersion: 1\n\nPackage: b\nVersion: 2",
			want: []Stanza{{"Package": "a", "Version": "1"}, {"Package": "b", "Version": "2"}},
		},
		{
			name: "continuation lines stay with their field",
			text: "Package: a\nDescription: short\n long one\n .\n long two\nSection: x\n",
			want: []Stanza{{"Package": "a", "Description": "short\nlong one\n.\nlong two", "Section": "x"}},
		},
		{
			name: "CRLF and several blank lines",
			text: "Package: a\r\n\r\n\r\nPackage: b\r\n",
			want: []Stanza{{"Package": "a"}, {"Package": "b"}},
		},
		{
			name: "lines without a colon are skipped",
			text: "garbage\nPackage: a\nmore garbage\n",
			want: []Stanza{{"Package": "a"}},
		},
		{
			name: "values keep colons after the first",
			text: "Homepage: https://curl.se/\n",
			want: []Stanza{{"Homepage": "https://curl.se/"}},
		},
		{
			name: "empty input",
			text: "\n\n",
			want: nil,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := collectStanzas(t, testCase.text)
			if len(got) != len(testCase.want) {
				t.Fatalf("got %d stanzas %v, want %d", len(got), got, len(testCase.want))
			}
			for i := range got {
				if len(got[i]) != len(testCase.want[i]) {
					t.Errorf("stanza %d = %v, want %v", i, got[i], testCase.want[i])
				}
				for field, wantValue := range testCase.want[i] {
					if got[i][field] != wantValue {
						t.Errorf("stanza %d field %s = %q, want %q", i, field, got[i][field], wantValue)
					}
				}
			}
		})
	}
}

func TestReadStanzasStopsAtVisitError(t *testing.T) {
	stopHere := errors.New("stop")
	visited := 0
	err := ReadStanzas(strings.NewReader("Package: a\n\nPackage: b\n\nPackage: c\n"), func(Stanza) error {
		visited++
		if visited == 2 {
			return stopHere
		}
		return nil
	})
	if !errors.Is(err, stopHere) || visited != 2 {
		t.Fatalf("err = %v, visited = %d; want the visitor's error after two stanzas", err, visited)
	}
}

func TestParseStanza(t *testing.T) {
	if _, err := ParseStanza("Package: a\n\nPackage: b\n"); err == nil {
		t.Error("two paragraphs must be an error")
	}
	if _, err := ParseStanza(""); err == nil {
		t.Error("no paragraph must be an error")
	}
	stanza, err := ParseStanza(sampleControl)
	if err != nil {
		t.Fatal(err)
	}
	if stanza["Package"] != "hello" || !strings.HasPrefix(stanza["Description"], "example package") {
		t.Errorf("stanza = %v", stanza)
	}
}

func TestReadIndexEntriesFromRealExcerpt(t *testing.T) {
	file, err := os.Open(filepath.Join("testdata", "Packages-excerpt"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }() // read-only: closing cannot lose data
	var entries []IndexEntry
	if err := ReadIndexEntries(file, func(entry IndexEntry) error {
		entries = append(entries, entry)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []IndexEntry{
		{Package: "curl", Version: "8.5.0-2ubuntu10", Architecture: "amd64", Filename: "pool/main/c/curl/curl_8.5.0-2ubuntu10_amd64.deb", Size: 226572, SHA256: "af7af42226d21bcbf87cf62a9e158bd5dfbd051ebbde8818ca441dc8085f67af"},
		{Package: "libc6", Version: "2.39-0ubuntu8", Architecture: "amd64", Filename: "pool/main/g/glibc/libc6_2.39-0ubuntu8_amd64.deb", Size: 3264806, SHA256: "af36c7ac770770fe3d3c10e85d6bc538e76e57570ba7db7d397fb9f654783ef3"},
		{Package: "tzdata", Version: "2024a-2ubuntu1", Architecture: "all", Filename: "pool/main/t/tzdata/tzdata_2024a-2ubuntu1_all.deb", Size: 273318, SHA256: "f5bca0c788a4fc465c435f7e8a54b2594e9cbf1a22abb263a59e393f1cb666e1"},
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(entries), len(want), entries)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entry %d =\n%+v\nwant\n%+v", i, entries[i], want[i])
		}
	}
}

func TestReadIndexEntriesRejectsUnreadableSize(t *testing.T) {
	err := ReadIndexEntries(strings.NewReader("Package: a\nVersion: 1\nSize: large\n"), func(IndexEntry) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "a 1") {
		t.Fatalf("err = %v, want one naming the package", err)
	}
}

func TestReadControlEveryCompression(t *testing.T) {
	// Ubuntu has shipped all of these: gzip before 2018, xz through focal,
	// zstd from jammy on.
	for _, extension := range []string{"", ".gz", ".xz", ".zst"} {
		t.Run("control.tar"+extension, func(t *testing.T) {
			path := buildDeb(t, t.TempDir(), "hello_2.10-3build1_amd64.deb", extension, sampleControl)
			control, err := ReadControl(path)
			if err != nil {
				t.Fatal(err)
			}
			if control.Text != strings.TrimRight(sampleControl, "\n") {
				t.Errorf("Text =\n%s\nwant the control file without its final newline", control.Text)
			}
			if control.Fields["Package"] != "hello" || control.Fields["Version"] != "2.10-3build1" || control.Fields["Architecture"] != "amd64" {
				t.Errorf("Fields = %v", control.Fields)
			}
		})
	}
}

func TestReadControlErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name string, content []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	goodDeb, err := os.ReadFile(buildDeb(t, dir, "good.deb", ".gz", sampleControl))
	if err != nil {
		t.Fatal(err)
	}
	testCases := []struct {
		name        string
		path        string
		wantNotDeb  bool
		wantInError string
	}{
		{name: "not an archive", path: writeFile("text.deb", []byte("hello world, not an archive at all")), wantNotDeb: true},
		{name: "empty file", path: writeFile("empty.deb", nil), wantNotDeb: true},
		{name: "truncated", path: writeFile("cut.deb", goodDeb[:len(goodDeb)/2]), wantNotDeb: true},
		{
			name:       "no control member",
			path:       writeFile("nocontrol.deb", debtest.WriteAr([]debtest.ArMember{{Name: "debian-binary", Data: []byte("2.0\n")}, {Name: "data.tar", Data: debtest.WriteTar(t, map[string]string{"f": "x"})}})),
			wantNotDeb: true,
		},
		{
			name:        "unsupported compression",
			path:        writeFile("bz2.deb", debtest.WriteAr([]debtest.ArMember{{Name: "debian-binary", Data: []byte("2.0\n")}, {Name: "control.tar.bz2", Data: []byte("BZh9")}})),
			wantInError: "unsupported compression",
		},
		{
			name:        "control without a version",
			path:        buildDeb(t, dir, "noversion.deb", ".zst", "Package: hello\nArchitecture: amd64\n"),
			wantInError: "lacks Version",
		},
		{
			name:        "control archive without a control file",
			path:        writeFile("nofile.deb", debtest.WriteAr([]debtest.ArMember{{Name: "debian-binary", Data: []byte("2.0\n")}, {Name: "control.tar", Data: debtest.WriteTar(t, map[string]string{"md5sums": ""})}})),
			wantInError: "no control file",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ReadControl(testCase.path)
			if err == nil {
				t.Fatal("ReadControl succeeded, want an error")
			}
			if testCase.wantNotDeb && !errors.Is(err, ErrNotDeb) {
				t.Errorf("err = %v, want ErrNotDeb", err)
			}
			if !strings.Contains(err.Error(), testCase.wantInError) {
				t.Errorf("err = %v, want it to mention %q", err, testCase.wantInError)
			}
			if !strings.Contains(err.Error(), filepath.Base(testCase.path)) {
				t.Errorf("err = %v, want it to name the file", err)
			}
		})
	}
}

func TestWriteFlatRepository(t *testing.T) {
	dir := t.TempDir()
	helloPath := buildDeb(t, dir, "hello_2.10-3build1_amd64.deb", ".zst", sampleControl)
	buildDeb(t, dir, "adduser_3.137ubuntu1_all.deb", ".xz", "Package: adduser\nVersion: 3.137ubuntu1\nArchitecture: all\nPriority: important\nDescription: add users\n")
	release := FlatRelease{Suite: "noble", Arch: "amd64", Date: time.Date(2026, 9, 17, 7, 49, 42, 0, time.UTC)}
	// Given out of order: the output must not depend on it.
	if err := WriteFlatRepository(dir, release, []string{"hello_2.10-3build1_amd64.deb", "adduser_3.137ubuntu1_all.deb"}); err != nil {
		t.Fatal(err)
	}

	packagesBytes, err := os.ReadFile(filepath.Join(dir, "Packages"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []IndexEntry
	if err := ReadIndexEntries(bytes.NewReader(packagesBytes), func(entry IndexEntry) error {
		entries = append(entries, entry)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	helloContent, err := os.ReadFile(helloPath)
	if err != nil {
		t.Fatal(err)
	}
	helloDigest := sha256.Sum256(helloContent)
	if len(entries) != 2 || entries[0].Package != "adduser" || entries[1].Package != "hello" {
		t.Fatalf("entries = %+v, want adduser then hello", entries)
	}
	hello := entries[1]
	if hello.Filename != "./hello_2.10-3build1_amd64.deb" || hello.Size != int64(len(helloContent)) || hello.SHA256 != hex.EncodeToString(helloDigest[:]) {
		t.Errorf("hello entry = %+v, want ./ file name, the file's size and its SHA-256", hello)
	}
	packagesText := string(packagesBytes)
	for _, wantText := range []string{"Priority: important\n", "MD5sum: ", " Seriously, though: this is an example of how to do a Debian package.\nFilename: ./hello"} {
		if !strings.Contains(packagesText, wantText) {
			t.Errorf("Packages lacks %q:\n%s", wantText, packagesText)
		}
	}
	if strings.Contains(packagesText, "\n\n\n") {
		t.Errorf("Packages has an empty paragraph:\n%s", packagesText)
	}

	releaseText, err := os.ReadFile(filepath.Join(dir, "Release"))
	if err != nil {
		t.Fatal(err)
	}
	packagesDigest := sha256.Sum256(packagesBytes)
	for _, wantLine := range []string{
		"Suite: noble\n", "Codename: noble\n", "Architectures: amd64\n", "Date: Thu, 17 Sep 2026 07:49:42 UTC\n",
		fmt.Sprintf("SHA256:\n %s %d Packages\n", hex.EncodeToString(packagesDigest[:]), len(packagesBytes)),
	} {
		if !strings.Contains(string(releaseText), wantLine) {
			t.Errorf("Release lacks %q:\n%s", wantLine, releaseText)
		}
	}

	// Deterministic: a second write produces the same bytes.
	if err := WriteFlatRepository(dir, release, []string{"adduser_3.137ubuntu1_all.deb", "hello_2.10-3build1_amd64.deb"}); err != nil {
		t.Fatal(err)
	}
	secondPackages, err := os.ReadFile(filepath.Join(dir, "Packages"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packagesBytes, secondPackages) {
		t.Error("two writes of the same repository differ")
	}
}

func TestWriteFlatRepositoryFailsOnBadPackage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.deb"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteFlatRepository(dir, FlatRelease{Suite: "noble", Arch: "amd64"}, []string{"broken.deb"})
	if !errors.Is(err, ErrNotDeb) {
		t.Fatalf("err = %v, want ErrNotDeb", err)
	}
}

func TestSHA256File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("frostroot"), 0o644); err != nil {
		t.Fatal(err)
	}
	size, digest, err := SHA256File(path)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("frostroot"))
	if size != 9 || digest != hex.EncodeToString(want[:]) {
		t.Errorf("SHA256File = %d, %s", size, digest)
	}
}
