package index

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// fuzzSeed returns the first limit bytes of a fixture, a seed small enough
// for the fuzzer to work with.
func fuzzSeed(f *testing.F, name string, limit int) []byte {
	f.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		f.Fatal(err)
	}
	return data[:min(len(data), limit)]
}

// FuzzParseRelease: a Release file is the first thing a mirror says.
func FuzzParseRelease(f *testing.F) {
	f.Add([]byte("Origin: Ubuntu\nSHA256:\n 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef 1234 main/binary-amd64/Packages.xz\n 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef 4321 main/binary-amd64/Packages\n"))
	f.Add([]byte("SHA256:\n x -1 y\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		listed, err := parseRelease(bytes.NewReader(data))
		if err != nil {
			return
		}
		for path, file := range listed {
			if file.size < 0 || file.openSize < 0 {
				t.Fatalf("%s listed with size %d, open size %d", path, file.size, file.openSize)
			}
		}
	})
}

// FuzzReadSimpleIndex: PyPI's simple index, or the one a recipe names.
func FuzzReadSimpleIndex(f *testing.F) {
	f.Add(fuzzSeed(f, "pypi-simple.json", 4096))
	f.Add([]byte(`{"meta":{"api-version":"1.1"},"projects":[{"name":"numpy"},{"name":""}]}`))
	f.Add([]byte(`{"projects":[{"name":{"x":1}}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = readSimpleIndex(bytes.NewReader(data))
	})
}

// FuzzFedoraMetadata: repomd.xml and the primary metadata it points to.
func FuzzFedoraMetadata(f *testing.F) {
	f.Add(fuzzSeed(f, "fedora-44.primary.xml", 8192))
	f.Add([]byte(`<repomd><data type="primary"><checksum type="sha256">00</checksum><location href="repodata/x-primary.xml.zst"/><size>1</size><open-size>2</open-size></data></repomd>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseRepomd(bytes.NewReader(data))
		_, _ = reducePrimary(bytes.NewReader(data))
	})
}
