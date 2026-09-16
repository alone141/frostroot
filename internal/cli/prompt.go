package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// linePrompt asks on out and reads one line per answer from in. End of input
// counts as accepting the default, so init also works with stdin closed.
type linePrompt struct {
	in  *bufio.Reader
	out io.Writer
}

func newLinePrompt(in io.Reader, out io.Writer) *linePrompt {
	return &linePrompt{in: bufio.NewReader(in), out: out}
}

func (p *linePrompt) Ask(question, defaultValue string) (string, error) {
	if defaultValue != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", question, defaultValue)
	} else {
		fmt.Fprintf(p.out, "%s: ", question)
	}
	line, err := p.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if errors.Is(err, io.EOF) && line == "" {
		fmt.Fprintln(p.out)
	}
	if answer := strings.TrimSpace(line); answer != "" {
		return answer, nil
	}
	return defaultValue, nil
}
