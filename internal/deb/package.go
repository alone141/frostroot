package deb

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// Control is the control file of a .deb package.
type Control struct {
	Text   string // the paragraph as written, without a trailing newline
	Fields Stanza
}

// Layout of the ar(1) format a .deb is stored in.
const (
	arMagic            = "!<arch>\n"
	arHeaderBytes      = 60
	arNameBytes        = 16
	arSizeOffset       = 48
	arSizeBytes        = 10
	arHeaderMagic      = "`\n"
	controlMemberName  = "control.tar"
	maxControlTarBytes = 64 << 20 // a control archive is kilobytes; this only bounds a corrupt size field
	maxControlBytes    = 1 << 20
)

// ErrNotDeb means the file is not a Debian package archive.
var ErrNotDeb = errors.New("not a .deb file")

// ReadControl returns the control file of the package at path. It reads the
// ar archive, finds the control.tar member whatever its compression (none,
// gzip, xz or zstd: Ubuntu has used all of them), and extracts "control"
// from it.
func ReadControl(path string) (Control, error) {
	file, err := os.Open(path)
	if err != nil {
		return Control{}, err
	}
	defer func() { _ = file.Close() }() // read-only: closing cannot lose data
	control, err := readControl(file)
	if err != nil {
		return Control{}, fmt.Errorf("%s: %w", path, err)
	}
	return control, nil
}

func readControl(archive io.Reader) (Control, error) {
	magic := make([]byte, len(arMagic))
	if _, err := io.ReadFull(archive, magic); err != nil || string(magic) != arMagic {
		return Control{}, ErrNotDeb
	}
	for {
		header := make([]byte, arHeaderBytes)
		if _, err := io.ReadFull(archive, header); err != nil {
			if errors.Is(err, io.EOF) {
				return Control{}, fmt.Errorf("%w: no %s member", ErrNotDeb, controlMemberName)
			}
			return Control{}, fmt.Errorf("%w: truncated member header", ErrNotDeb)
		}
		if string(header[arHeaderBytes-len(arHeaderMagic):]) != arHeaderMagic {
			return Control{}, fmt.Errorf("%w: bad member header", ErrNotDeb)
		}
		// GNU ar may end a name with a slash; .deb members never contain one.
		memberName := strings.TrimSuffix(strings.TrimRight(string(header[:arNameBytes]), " "), "/")
		memberSize, err := strconv.ParseInt(strings.TrimSpace(string(header[arSizeOffset:arSizeOffset+arSizeBytes])), 10, 64)
		if err != nil || memberSize < 0 {
			return Control{}, fmt.Errorf("%w: bad size for member %q", ErrNotDeb, memberName)
		}
		if strings.HasPrefix(memberName, controlMemberName) {
			if memberSize > maxControlTarBytes {
				return Control{}, fmt.Errorf("%s is %d bytes, too large for a control archive", memberName, memberSize)
			}
			compressed := make([]byte, memberSize)
			if _, err := io.ReadFull(archive, compressed); err != nil {
				return Control{}, fmt.Errorf("%w: truncated %s", ErrNotDeb, memberName)
			}
			return controlFromTar(memberName, compressed)
		}
		// Members are padded to an even size.
		if _, err := io.CopyN(io.Discard, archive, memberSize+memberSize%2); err != nil {
			return Control{}, fmt.Errorf("%w: truncated member %q", ErrNotDeb, memberName)
		}
	}
}

// controlFromTar decompresses a control.tar member by its name's extension
// and returns the control file inside it.
func controlFromTar(memberName string, compressed []byte) (Control, error) {
	var decompressed io.Reader
	switch extension := strings.TrimPrefix(memberName, controlMemberName); extension {
	case "":
		decompressed = bytes.NewReader(compressed)
	case ".gz":
		gzipReader, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return Control{}, fmt.Errorf("%s: %w", memberName, err)
		}
		decompressed = gzipReader
	case ".xz":
		xzReader, err := xz.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return Control{}, fmt.Errorf("%s: %w", memberName, err)
		}
		decompressed = xzReader
	case ".zst":
		zstdReader, err := zstd.NewReader(bytes.NewReader(compressed), zstd.WithDecoderConcurrency(1))
		if err != nil {
			return Control{}, fmt.Errorf("%s: %w", memberName, err)
		}
		defer zstdReader.Close()
		decompressed = zstdReader
	default:
		return Control{}, fmt.Errorf("%s: unsupported compression %q", memberName, extension)
	}
	tarReader := tar.NewReader(decompressed)
	for {
		entry, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return Control{}, fmt.Errorf("%s holds no control file", memberName)
		}
		if err != nil {
			return Control{}, fmt.Errorf("%s: %w", memberName, err)
		}
		if strings.TrimPrefix(entry.Name, "./") != "control" {
			continue
		}
		text, err := io.ReadAll(io.LimitReader(tarReader, maxControlBytes))
		if err != nil {
			return Control{}, fmt.Errorf("%s: reading control: %w", memberName, err)
		}
		return parseControl(string(text))
	}
}

// parseControl checks that text is one paragraph naming a package.
func parseControl(text string) (Control, error) {
	text = strings.TrimRight(text, "\n")
	fields, err := ParseStanza(text)
	if err != nil {
		return Control{}, fmt.Errorf("control file: %w", err)
	}
	for _, requiredField := range []string{"Package", "Version", "Architecture"} {
		if fields[requiredField] == "" {
			return Control{}, fmt.Errorf("control file lacks %s", requiredField)
		}
	}
	return Control{Text: text, Fields: fields}, nil
}
