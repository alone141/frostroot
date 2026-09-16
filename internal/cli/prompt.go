package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// linePrompt asks questions on output and reads one line per answer from
// input. End of input counts as accepting the default, so init also works with
// standard input closed.
type linePrompt struct {
	input  *bufio.Reader
	output io.Writer
}

func newLinePrompt(input io.Reader, output io.Writer) *linePrompt {
	return &linePrompt{input: bufio.NewReader(input), output: output}
}

// Ask implements Prompt.
func (p *linePrompt) Ask(question, defaultAnswer string) (string, error) {
	questionText := question + ": "
	if defaultAnswer != "" {
		questionText = fmt.Sprintf("%s [%s]: ", question, defaultAnswer)
	}
	if _, err := io.WriteString(p.output, questionText); err != nil {
		return "", err
	}
	line, err := p.input.ReadString('\n')
	atEndOfInput := errors.Is(err, io.EOF)
	if err != nil && !atEndOfInput {
		return "", err
	}
	if atEndOfInput && line == "" {
		// Nothing was typed, so end the question's line ourselves.
		if _, err := io.WriteString(p.output, "\n"); err != nil {
			return "", err
		}
	}
	if answer := strings.TrimSpace(line); answer != "" {
		return answer, nil
	}
	return defaultAnswer, nil
}
