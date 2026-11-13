package deb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

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

// controlOfSize returns a control file of exactly size bytes whose
// Description is folded over as many lines as that takes.
func controlOfSize(t *testing.T, size int) string {
	t.Helper()
	var control strings.Builder
	control.WriteString("Package: hello\nVersion: 1\nArchitecture: amd64\nDescription: long\n")
	for control.Len() < size {
		remaining := size - control.Len()
		lineLength := min(64, remaining)
		if remaining > 64 && remaining < 64+3 {
			lineLength = 32 // so that the last line is not too short to hold a character
		}
		if lineLength < 3 {
			t.Fatalf("cannot make a control file of exactly %d bytes", size)
		}
		control.WriteString(" " + strings.Repeat("x", lineLength-2) + "\n")
	}
	return control.String()
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
		{
			name: "a tab starts a continuation, and only the first blank goes",
			text: "Description: short\n\tone\n \ttwo\n  three\n",
			want: []Stanza{{"Description": "short\none\n\ttwo\n three"}},
		},
		{
			name: "CRLF in a folded field",
			text: "Description: short\r\n one\r\n .\r\n two\r\n",
			want: []Stanza{{"Description": "short\none\n.\ntwo"}},
		},
		{
			name: "a folded field with an empty first line",
			text: "Description:\n .\n x\n",
			want: []Stanza{{"Description": "\n.\nx"}},
		},
		{
			name: "a continuation before any field is dropped",
			text: " stray\nPackage: a\n\n stray after a paragraph\nPackage: b\n",
			want: []Stanza{{"Package": "a"}, {"Package": "b"}},
		},
		{
			name: "a field given twice keeps the last, with its own continuation",
			text: "Depends: one\n first\nPackage: a\nDepends: two\n second\n",
			want: []Stanza{{"Depends": "two\nsecond", "Package": "a"}},
		},
		{
			name: "a field given twice keeps the last even when only the first is folded",
			text: "Depends: one\n first\nDepends: two\n",
			want: []Stanza{{"Depends": "two"}},
		},
		{
			name: "a skipped line does not end the field before it",
			text: "Description: short\ngarbage\n more\n",
			want: []Stanza{{"Description": "short\nmore"}},
		},
		{
			name: "a field with an empty name takes no continuation",
			text: ": x\n y\nPackage: a\n",
			want: []Stanza{{"": "x", "Package": "a"}},
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

// TestReadStanzasLongFoldedFieldTakesLinearTime: a Packages index comes from
// the network, and a field folded over many lines must not cost time in the
// square of its length.
func TestReadStanzasLongFoldedFieldTakesLinearTime(t *testing.T) {
	const continuationLines = 400_000
	var text strings.Builder
	text.WriteString("Package: a\nDescription: long\n")
	for range continuationLines {
		text.WriteString(" xx\n")
	}
	text.WriteString("Section: x\n")
	start := time.Now()
	stanzas := collectStanzas(t, text.String())
	elapsed := time.Since(start)
	t.Logf("reading %d continuation lines took %v", continuationLines, elapsed)
	if elapsed > 5*time.Second {
		t.Errorf("reading %d continuation lines took %v, want well under 5s", continuationLines, elapsed)
	}
	if len(stanzas) != 1 {
		t.Fatalf("got %d stanzas, want 1", len(stanzas))
	}
	description := stanzas[0]["Description"]
	if wantLength := len("long") + continuationLines*len("\nxx"); len(description) != wantLength {
		t.Errorf("Description is %d bytes, want %d", len(description), wantLength)
	}
	if stanzas[0]["Section"] != "x" {
		t.Errorf("Section = %q, want %q", stanzas[0]["Section"], "x")
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

func TestReadControlAcceptsAControlFileAtTheLimit(t *testing.T) {
	control := controlOfSize(t, maxControlBytes)
	got, err := ReadControl(buildDeb(t, t.TempDir(), "long.deb", ".gz", control))
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != strings.TrimRight(control, "\n") {
		t.Errorf("Text is %d bytes, want the %d of the control file without its final newline", len(got.Text), len(control)-1)
	}
}

// TestReadControlRefusesALargeWindow: a .deb is read before anything checks
// it against the lock, and a few bytes of its control archive may declare
// the memory the decoder is to allocate.
func TestReadControlRefusesALargeWindow(t *testing.T) {
	controlTar := debtest.WriteTar(t, map[string]string{"control": sampleControl})
	// A zstd frame of one raw block whose header declares a 512 MiB window,
	// the most the decoder allows by default: magic, frame header
	// descriptor, window descriptor, block header, block.
	zstdFrame := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x98}
	zstdFrame = append(zstdFrame, byte(1|len(controlTar)<<3), byte(len(controlTar)>>5), byte(len(controlTar)>>13))
	zstdFrame = append(zstdFrame, controlTar...)
	// An xz stream whose one block declares a dictionary of 4 GiB - 1.
	xzStream := withDictionaryCode(t, debtest.Compress(t, ".xz", controlTar), xzStreamHeaderBytes, 40)
	testCases := []struct {
		name       string
		member     string
		compressed []byte
		wantErr    error
	}{
		{name: "zstd", member: "control.tar.zst", compressed: zstdFrame, wantErr: zstd.ErrWindowSizeExceeded},
		{name: "xz", member: "control.tar.xz", compressed: xzStream, wantErr: ErrXZDictionaryTooLarge},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hello.deb")
			archive := debtest.WriteAr([]debtest.ArMember{{Name: "debian-binary", Data: []byte("2.0\n")}, {Name: testCase.member, Data: testCase.compressed}})
			if err := os.WriteFile(path, archive, 0o644); err != nil {
				t.Fatal(err)
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, err := ReadControl(path)
			runtime.ReadMemStats(&after)
			if !errors.Is(err, testCase.wantErr) {
				t.Errorf("err = %v, want %v", err, testCase.wantErr)
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
				t.Errorf("allocated %d MiB, want far less than the window", allocated>>20)
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
	overLimit := controlOfSize(t, maxControlBytes+1)
	testCases := []struct {
		name        string
		path        string
		wantNotDeb  bool
		wantInError string
	}{
		{name: "not an archive", path: writeFile("text.deb", []byte("hello world, not an archive at all")), wantNotDeb: true},
		{name: "empty file", path: writeFile("empty.deb", nil), wantNotDeb: true},
		{
			name:        "control file one byte over the limit",
			path:        buildDeb(t, dir, "long.deb", ".gz", overLimit),
			wantInError: fmt.Sprintf("larger than %d bytes", maxControlBytes),
		},
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
	if err := WriteFlatRepository(dir, release, []string{"hello_2.10-3build1_amd64.deb", "adduser_3.137ubuntu1_all.deb"}, nil); err != nil {
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
	if err := WriteFlatRepository(dir, release, []string{"adduser_3.137ubuntu1_all.deb", "hello_2.10-3build1_amd64.deb"}, nil); err != nil {
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

// apt trusts a flat repository's index, and the index is what the files
// hashed to when it was written; a digest given for a file is checked first,
// and nothing is written when one differs.
func TestWriteFlatRepositoryChecksTheDigestsItIsGiven(t *testing.T) {
	dir := t.TempDir()
	helloPath := buildDeb(t, dir, "hello_2.10-3build1_amd64.deb", ".zst", sampleControl)
	buildDeb(t, dir, "adduser_3.137ubuntu1_all.deb", ".xz", "Package: adduser\nVersion: 3.137ubuntu1\nArchitecture: all\nPriority: important\nDescription: add users\n")
	helloContent, err := os.ReadFile(helloPath)
	if err != nil {
		t.Fatal(err)
	}
	helloDigest := sha256.Sum256(helloContent)
	names := []string{"hello_2.10-3build1_amd64.deb", "adduser_3.137ubuntu1_all.deb"}
	release := FlatRelease{Suite: "noble", Arch: "amd64"}
	if err := WriteFlatRepository(dir, release, names, map[string]string{"hello_2.10-3build1_amd64.deb": hex.EncodeToString(helloDigest[:])}); err != nil {
		t.Fatalf("the digest the file has: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "Packages")); err != nil {
		t.Fatal(err)
	}
	err = WriteFlatRepository(dir, release, names, map[string]string{"hello_2.10-3build1_amd64.deb": strings.Repeat("0", 64)})
	if !errors.Is(err, ErrDigestMismatch) || !strings.Contains(err.Error(), "hello_2.10-3build1_amd64.deb") {
		t.Fatalf("err = %v, want ErrDigestMismatch naming the file", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Packages")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an index was written for a file that is not the one expected: Stat error = %v", err)
	}
}

func TestWriteFlatRepositoryFailsOnBadPackage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.deb"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteFlatRepository(dir, FlatRelease{Suite: "noble", Arch: "amd64"}, []string{"broken.deb"}, nil)
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
