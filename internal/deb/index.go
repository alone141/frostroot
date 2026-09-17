package deb

import (
	"fmt"
	"io"
	"strconv"
)

// IndexEntry is what a Packages index says about one .deb file: which
// package it is, where it lives below the mirror, and how to verify it.
type IndexEntry struct {
	Package      string
	Version      string
	Architecture string
	Filename     string // path below the mirror's base URL, such as pool/main/c/curl/curl_8.5.0-2ubuntu10_amd64.deb
	Size         int64
	SHA256       string // lowercase hex
}

// ReadIndexEntries streams the entries of a Packages index to visit.
// Paragraphs without a Package field are skipped; an unreadable Size is an
// error naming the package, because a size that cannot be checked would let
// a truncated download pass.
func ReadIndexEntries(reader io.Reader, visit func(IndexEntry) error) error {
	return ReadStanzas(reader, func(stanza Stanza) error {
		packageName := stanza["Package"]
		if packageName == "" {
			return nil
		}
		entry := IndexEntry{
			Package:      packageName,
			Version:      stanza["Version"],
			Architecture: stanza["Architecture"],
			Filename:     stanza["Filename"],
			SHA256:       stanza["SHA256"],
		}
		if sizeText := stanza["Size"]; sizeText != "" {
			size, err := strconv.ParseInt(sizeText, 10, 64)
			if err != nil {
				return fmt.Errorf("package %s %s: unreadable Size %q", packageName, entry.Version, sizeText)
			}
			entry.Size = size
		}
		return visit(entry)
	})
}
