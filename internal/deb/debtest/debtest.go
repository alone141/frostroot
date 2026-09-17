// Package debtest builds small but well-formed .deb files for tests: an ar
// archive holding debian-binary, control.tar and data.tar, compressed the
// way dpkg-deb compresses them on each Ubuntu release.
package debtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// ArMember is one file inside an ar archive.
type ArMember struct {
	Name string
	Data []byte
}

// WriteAr encodes members in the ar format dpkg-deb uses.
func WriteAr(members []ArMember) []byte {
	var archive bytes.Buffer
	archive.WriteString("!<arch>\n")
	for _, member := range members {
		fmt.Fprintf(&archive, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", member.Name, 0, 0, 0, "100644", len(member.Data))
		archive.Write(member.Data)
		if len(member.Data)%2 == 1 {
			archive.WriteByte('\n')
		}
	}
	return archive.Bytes()
}

// WriteTar encodes files as a tar archive with "./" names, as dpkg-deb does.
func WriteTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var archive bytes.Buffer
	tarWriter := tar.NewWriter(&archive)
	for name, content := range files {
		header := &tar.Header{Name: "./" + name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

// Compress encodes data with the compressor a control.tar extension names:
// "" for none, ".gz", ".xz" or ".zst".
func Compress(t *testing.T, extension string, data []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	switch extension {
	case "":
		return data
	case ".gz":
		gzipWriter := gzip.NewWriter(&compressed)
		if _, err := gzipWriter.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := gzipWriter.Close(); err != nil {
			t.Fatal(err)
		}
	case ".xz":
		xzWriter, err := xz.NewWriter(&compressed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := xzWriter.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := xzWriter.Close(); err != nil {
			t.Fatal(err)
		}
	case ".zst":
		zstdWriter, err := zstd.NewWriter(&compressed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := zstdWriter.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := zstdWriter.Close(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("no compressor for %q", extension)
	}
	return compressed.Bytes()
}

// Build writes a .deb file named fileName into dir whose control.tar uses
// the given compression extension and holds control, and returns its path.
func Build(t *testing.T, dir, fileName, extension, control string) string {
	t.Helper()
	controlTar := Compress(t, extension, WriteTar(t, map[string]string{"control": control, "md5sums": ""}))
	dataTar := Compress(t, extension, WriteTar(t, map[string]string{"usr/bin/" + fileName: "#!/bin/sh\necho hello\n"}))
	archive := WriteAr([]ArMember{
		{Name: "debian-binary", Data: []byte("2.0\n")},
		{Name: "control.tar" + extension, Data: controlTar},
		{Name: "data.tar" + extension, Data: dataTar},
	})
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Control returns a minimal control file for a package.
func Control(packageName, version, arch string) string {
	return fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: Test <test@example.com>\nPriority: optional\nDescription: test package %s\n", packageName, version, arch, packageName)
}
