package builder

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"slices"
)

// ErrorLine is the first line of a build log that explains its failure.
type ErrorLine struct {
	Number int // one-based, in the log
	Text   string
}

// explanationPrefixes are how the tools a build runs announce an error:
// apt and mmdebstrap with "E:", dpkg with "dpkg: error", pip with "ERROR:",
// and frostroot's own provision and Python scripts with "frostroot:".
// Warnings ("W:") are not errors, and a build that failed had one.
var explanationPrefixes = []string{"E: ", "dpkg: error", "ERROR: ", "frostroot: "}

// mkosiExplanationPrefixes are how a Fedora build's tools announce an error:
// dnf5 names a package it cannot find or a transaction it cannot resolve,
// rpm a scriptlet that failed, and frostroot's own scripts say "frostroot:".
var mkosiExplanationPrefixes = []string{"No match for argument", "Failed to resolve the transaction", "Problem: ", "error: ", "frostroot: "}

// maxExplanationLineBytes bounds an explanation, with its newline; pip has
// been seen to print a single-line dependency report longer than that, which
// is not the explanation anyone wants. A longer line is never held in
// memory whole, and still counts as one line.
const maxExplanationLineBytes = 1 << 16

// FirstErrorLine returns the first line of the log at path that carries one
// of the prefixes an error is announced with, or false when there is none,
// or when the log cannot be read: a failure to explain a failure is not
// worth a second error message. The whole log is read, not its tail,
// because the line that explains a failure is often followed by more
// output than the tail holds — pip's traceback, apt's cleanup — and that is
// exactly when it is needed. A line too long to be an explanation that
// announces no error is passed over, and the lines after it are read. One
// that does announce an error is the first error line, and too long to
// show: then there is no explanation, rather than a later error line
// presented as the first, which may only be a consequence of it.
func FirstErrorLine(path string) (ErrorLine, bool) {
	file, err := os.Open(path)
	if err != nil {
		return ErrorLine{}, false
	}
	defer func() { _ = file.Close() }() // read only

	prefixes := explanationPrefixes
	if filepath.Base(path) == MkosiLogFileName {
		prefixes = mkosiExplanationPrefixes
	}
	announcesError := func(text []byte) bool {
		return slices.ContainsFunc(prefixes, func(prefix string) bool { return bytes.HasPrefix(text, []byte(prefix)) })
	}
	reader := bufio.NewReaderSize(file, maxExplanationLineBytes)
	number := 0
	for {
		// ReadLine drops the "\n" or "\r\n", and returns a last line that
		// has no newline as any other. A line longer than its buffer comes
		// in fragments, the first of them its start.
		fragment, isPrefix, err := reader.ReadLine()
		if err != nil {
			return ErrorLine{}, false
		}
		number++
		if isPrefix {
			if announcesError(fragment) {
				return ErrorLine{}, false
			}
			for isPrefix && err == nil {
				_, isPrefix, err = reader.ReadLine()
			}
			if err != nil {
				return ErrorLine{}, false
			}
			continue
		}
		line := bytes.TrimRight(fragment, "\r")
		if announcesError(line) {
			return ErrorLine{Number: number, Text: string(line)}, true
		}
	}
}
