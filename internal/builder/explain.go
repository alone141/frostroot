package builder

import (
	"bufio"
	"os"
	"strings"
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

// maxExplanationLineBytes bounds a log line; pip has been seen to print a
// single-line dependency report longer than that, which is not the
// explanation anyone wants.
const maxExplanationLineBytes = 1 << 16

// FirstErrorLine returns the first line of the log at path that carries one
// of the prefixes an error is announced with, or false when there is none,
// or when the log cannot be read: a failure to explain a failure is not
// worth a second error message. The whole log is read, not its tail,
// because the line that explains a failure is often followed by more
// output than the tail holds — pip's traceback, apt's cleanup — and that is
// exactly when it is needed.
func FirstErrorLine(path string) (ErrorLine, bool) {
	file, err := os.Open(path)
	if err != nil {
		return ErrorLine{}, false
	}
	defer func() { _ = file.Close() }() // read only

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxExplanationLineBytes)
	number := 0
	for scanner.Scan() {
		number++
		line := strings.TrimRight(scanner.Text(), "\r")
		for _, prefix := range explanationPrefixes {
			if strings.HasPrefix(line, prefix) {
				return ErrorLine{Number: number, Text: line}, true
			}
		}
	}
	return ErrorLine{}, false
}
