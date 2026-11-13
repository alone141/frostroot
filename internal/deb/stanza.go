// Package deb reads and writes the Debian package formats frostroot meets:
// control-file paragraphs (the dpkg status file, apt's Packages indexes), the
// .deb archive itself, and a flat apt repository. It knows nothing about
// recipes or locks.
package deb

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// maxLineBytes bounds one line of a control file. Depends and Description
// fields can be long; apt's own limit is far below this.
const maxLineBytes = 4 << 20

// Stanza is one paragraph of a control file: field names to values, names
// as written. Continuation lines, those starting with a space or a tab, are
// kept in the value joined by newlines.
type Stanza map[string]string

// ReadStanzas parses control-file paragraphs, separated by blank lines, and
// calls visit for each one as it is complete, so that an index of tens of
// megabytes is never held in memory at once. Reading stops at the first
// error visit returns, which is then returned. Lines that are neither a field
// nor a continuation are skipped, as dpkg does.
func ReadStanzas(reader io.Reader, visit func(Stanza) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	current := Stanza{}
	lastField := ""
	// The open field's value is built here and stored when the field ends,
	// because appending each continuation line to the stored string copies
	// the whole value every time: a field folded over a hundred thousand
	// lines took seconds.
	var lastValue strings.Builder
	storeLastField := func() {
		if lastField != "" {
			current[lastField] = lastValue.String()
		}
		lastField = ""
		lastValue.Reset()
	}
	finish := func() error {
		storeLastField()
		if len(current) == 0 {
			return nil
		}
		complete := current
		current = Stanza{}
		return visit(complete)
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		switch {
		case strings.TrimSpace(line) == "":
			if err := finish(); err != nil {
				return err
			}
		case line[0] == ' ' || line[0] == '\t':
			if lastField != "" {
				lastValue.WriteByte('\n')
				lastValue.WriteString(line[1:])
			}
		default:
			name, value, isField := strings.Cut(line, ":")
			if !isField {
				continue
			}
			storeLastField()
			current[name] = strings.TrimSpace(value)
			lastField = name
			lastValue.WriteString(current[name])
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading control file: %w", err)
	}
	return finish()
}

// ParseStanza parses text holding exactly one paragraph, such as a .deb's
// control file.
func ParseStanza(text string) (Stanza, error) {
	var stanzas []Stanza
	err := ReadStanzas(strings.NewReader(text), func(stanza Stanza) error {
		stanzas = append(stanzas, stanza)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(stanzas) != 1 {
		return nil, fmt.Errorf("expected one paragraph, found %d", len(stanzas))
	}
	return stanzas[0], nil
}
