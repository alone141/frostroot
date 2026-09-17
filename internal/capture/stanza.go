package capture

import (
	"bufio"
	"io"
	"strings"
)

// stanza is one paragraph of a Debian control file: field names to values.
// Continuation lines (those starting with a space) are kept in the value,
// joined with newlines, so Conffiles lists can be read line by line.
type stanza map[string]string

// readStanzas parses control-file paragraphs separated by blank lines, as in
// the dpkg status file, apt's extended_states and its Packages indexes.
// Malformed lines are skipped; a corrupt file yields fewer stanzas, not an
// error, because capture reports what it finds rather than refusing to run.
func readStanzas(reader io.Reader) []stanza {
	var stanzas []stanza
	current := stanza{}
	lastField := ""
	flush := func() {
		if len(current) > 0 {
			stanzas = append(stanzas, current)
			current = stanza{}
		}
		lastField = ""
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.TrimSpace(line) == "":
			flush()
		case line[0] == ' ' || line[0] == '\t':
			if lastField != "" {
				current[lastField] += "\n" + strings.TrimRight(line[1:], "\r")
			}
		default:
			name, value, found := strings.Cut(line, ":")
			if !found {
				continue
			}
			lastField = name
			current[name] = strings.TrimSpace(value)
		}
	}
	flush()
	return stanzas
}

// continuationLines returns the lines of a multi-line field value after the
// first line, trimmed.
func continuationLines(value string) []string {
	lines := strings.Split(value, "\n")
	if len(lines) <= 1 {
		return nil
	}
	trimmed := make([]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		if line = strings.TrimSpace(line); line != "" {
			trimmed = append(trimmed, line)
		}
	}
	return trimmed
}
