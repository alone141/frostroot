package deb

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frostroot/internal/deb/debtest"
)

// FuzzReadStanzas: Packages indexes and dpkg's status come from the
// network and the image, and a stanza reader that panics or loops on one
// takes the build with it.
func FuzzReadStanzas(f *testing.F) {
	excerpt, err := os.ReadFile(filepath.Join("testdata", "Packages-excerpt"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(excerpt)
	f.Add([]byte(sampleControl))
	f.Add([]byte("Package: a\n continued\n\n\nPackage: b\nDescription:\n .\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = ReadStanzas(bytes.NewReader(data), func(stanza Stanza) error { return nil })
		_ = ReadIndexEntries(bytes.NewReader(data), func(IndexEntry) error { return nil })
		_, _ = ParseStanza(string(data))
	})
}

// FuzzReadControl: a .deb is read before anything checks it against the
// lock, by vendor's pool and an offline build's repository.
func FuzzReadControl(f *testing.F) {
	dir := f.TempDir()
	for _, extension := range []string{".gz", ".xz", ".zst"} {
		data, err := os.ReadFile(debtest.Build(f, dir, "hello"+extension+".deb", extension, sampleControl))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(arMagic))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = readControl(bytes.NewReader(data))
	})
}

// FuzzCompareVersions: dpkg's order is a total one, whatever a Packages
// index says a version is.
func FuzzCompareVersions(f *testing.F) {
	for _, pair := range [][2]string{{"1.0", "1.0~rc1"}, {"1:2.34.1-1ubuntu1.11", "2.34.1"}, {"8.5.0-2ubuntu10", "8.5.0-2ubuntu10.1"}, {"", "0"}, {"1.0+b1", "1.0-1"}} {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if strings.ContainsAny(a+b, "\x00") {
			return
		}
		if ab, ba := CompareVersions(a, b), CompareVersions(b, a); ab != -ba {
			t.Fatalf("CompareVersions(%q, %q) = %d, but (%q, %q) = %d", a, b, ab, b, a, ba)
		}
		if same := CompareVersions(a, a); same != 0 {
			t.Fatalf("CompareVersions(%q, itself) = %d", a, same)
		}
	})
}
