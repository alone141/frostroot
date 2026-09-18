package tui

import (
	"strings"
	"testing"

	"frostroot/internal/form"
)

func TestNoteTextEscapesWhatANoteReadsAsMarkup(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "en_US.UTF-8", want: `en\_US.UTF-8`},
		{in: "America/Argentina/Buenos_Aires", want: `America/Argentina/Buenos\_Aires`},
		{in: "*bold* and `code`", want: "\\*bold\\* and \\`code\\`"},
		{in: `a\b`, want: `a\\b`},
		{in: "plain text, no markup", want: "plain text, no markup"},
	}
	for _, test := range tests {
		if got := noteText(test.in); got != test.want {
			t.Errorf("noteText(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestSummaryPageShowsTheLocaleAsItIs(t *testing.T) {
	// The locale's underscore turned the rest of the page italic and went
	// missing; the frame form-summary-80x24 pins the rendering.
	d := newFormDriver(t, form.Defaults(noHost))
	d.pressEnterUntil(stageSummary)
	if view := frameOf(d.model.View()); !strings.Contains(view, "en_US.UTF-8") {
		t.Errorf("the summary does not show en_US.UTF-8:\n%s", view)
	}
}
