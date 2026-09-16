package cli

import (
	"fmt"
	"io"

	"frostroot/internal/builder"
)

// plainProgress prints a build's progress as lines: one when a phase starts,
// one at every tenth of a measured phase. It is what a build shows without a
// terminal, in a log, or with --plain. Every line of mmdebstrap's own output
// goes to the log file in the work directory instead.
type plainProgress struct {
	output    io.Writer
	lastTenth int64 // tenths of the running phase already printed
}

func newPlainProgress(output io.Writer) *plainProgress {
	return &plainProgress{output: output}
}

// Report implements builder.Progress.
func (p *plainProgress) Report(event builder.ProgressEvent) {
	switch event.Kind {
	case builder.EventPhaseStarted:
		p.lastTenth = 0
		p.printf("frostroot: %s\n", event.Phase.Title())
	case builder.EventProgress:
		percent, known := event.Percent()
		if !known {
			return
		}
		tenth := int64(percent / 10)
		if tenth <= p.lastTenth {
			return
		}
		p.lastTenth = tenth
		p.printf("frostroot:   %3d%%  %s\n", tenth*10, event.Summary())
	case builder.EventLogFile:
		p.printf("frostroot: mmdebstrap output goes to %s\n", event.Line)
	case builder.EventLogLine, builder.EventPhaseFinished:
	}
}

// printf writes to the progress output; see App.stdoutf about the discarded
// error.
func (p *plainProgress) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(p.output, format, args...)
}
