package cli

import (
	"bufio"
	"context"
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
	// interrupted, when set, ends a question still waiting for its answer:
	// Ask returns context.Canceled, and every later question does too. A
	// command that wrote files before its form sets it, so that Ctrl-C at
	// a question runs that command's cleanup rather than killing it first.
	interrupted <-chan struct{}
	ended       bool
}

// lineRead is one line read from the input, or why none was.
type lineRead struct {
	line string
	err  error
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
	if p.ended {
		return "", context.Canceled
	}
	if _, err := io.WriteString(p.output, questionText); err != nil {
		return "", err
	}
	// The read waits in a goroutine of its own, so that an interrupt can
	// end the question; a nil channel never fires, and then this is only
	// a read. An interrupted read is left behind, and nothing reads after
	// it: the prompt has ended.
	reads := make(chan lineRead, 1)
	go func() {
		line, err := p.input.ReadString('\n')
		reads <- lineRead{line: line, err: err}
	}()
	var read lineRead
	select {
	case read = <-reads:
	case <-p.interrupted:
		p.ended = true
		_, _ = io.WriteString(p.output, "\n") // ending the question's line; the interrupt is the news
		return "", context.Canceled
	}
	line, err := read.line, read.err
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
