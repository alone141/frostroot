package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"frostroot/internal/form"
)

// previewOf returns a PreviewFunc that shows text under heading.
func previewOf(heading, text string, unchanged bool) PreviewFunc {
	return func(form.Values) Preview { return Preview{Heading: heading, Text: text, Unchanged: unchanged} }
}

func TestSummaryPageShowsThePreviewAndScrollsIt(t *testing.T) {
	lines := make([]string, 60)
	for index := range lines {
		lines[index] = "line " + strings.Repeat("x", index%7) + " " + string(rune('a'+index%26))
	}
	d := newFormDriverWithPreview(t, form.Defaults(noHost), previewOf("This is what frostroot.toml will say:", strings.Join(lines, "\n"), false))
	d.press(tea.WindowSizeMsg{Width: 80, Height: 24})
	d.pressEnterUntil(stageSummary)

	view := frameOf(d.model.View())
	for _, wantText := range []string{"Summary", "lab, Ubuntu 24.04 amd64", "This is what frostroot.toml will say:", "line  a", "more lines", "Write frostroot.toml?"} {
		if !strings.Contains(view, wantText) {
			t.Errorf("summary page lacks %q:\n%s", wantText, view)
		}
	}
	if strings.Contains(view, "line xxxxxx g") { // the 7th line is beyond a 3-row pane on 24 rows
		t.Errorf("the pane shows more than fits:\n%s", view)
	}

	d.press(tea.KeyMsg{Type: tea.KeyPgDown})
	scrolled := frameOf(d.model.View())
	if scrolled == view {
		t.Error("PgDn did not scroll the pane")
	}
	if d.model.stage != stageSummary || !d.model.write {
		t.Errorf("scrolling changed the answer: stage %d, write %v", d.model.stage, d.model.write)
	}

	// The question still takes its own keys after scrolling.
	d.press(pressLeft)
	d.press(pressEnter)
	if !d.quit || d.model.write {
		t.Errorf("quit = %v, write = %v; want declined", d.quit, d.model.write)
	}
}

func TestSummaryPageDefaultsToNoWhenNothingChanges(t *testing.T) {
	d := newFormDriverWithPreview(t, form.Defaults(noHost), previewOf("Nothing changes: frostroot.toml already says this.", "[image]\nname = \"lab\"\n", true))
	d.press(tea.WindowSizeMsg{Width: 80, Height: 24})
	d.pressEnterUntil(stageSummary)
	if view := frameOf(d.model.View()); !strings.Contains(view, "Nothing changes. Write anyway?") {
		t.Errorf("the question does not say nothing changes:\n%s", view)
	}
	if d.model.write {
		t.Fatal("the answer should start as no when nothing changes")
	}
	d.press(pressEnter)
	if !d.quit || d.model.write {
		t.Errorf("quit = %v, write = %v; want finished without writing", d.quit, d.model.write)
	}
}

func TestSummaryPageWithoutAPreviewIsTheSummaryAlone(t *testing.T) {
	d := newFormDriver(t, form.Defaults(noHost))
	d.press(tea.WindowSizeMsg{Width: 80, Height: 24})
	d.pressEnterUntil(stageSummary)
	view := frameOf(d.model.View())
	if strings.Contains(view, "╭") || strings.Contains(view, "scroll") {
		t.Errorf("a page with no preview drew a pane:\n%s", view)
	}
	if !strings.Contains(view, "Write frostroot.toml?") || !d.model.write {
		t.Errorf("the question is not there or does not start as yes:\n%s", view)
	}
}

func TestSummaryPaneFitsTheTerminal(t *testing.T) {
	d := newFormDriverWithPreview(t, form.Defaults(noHost), previewOf("heading", strings.Repeat("x\n", 100), false))
	d.press(tea.WindowSizeMsg{Width: 120, Height: 40})
	d.pressEnterUntil(stageSummary)
	tall := d.model.summary.pane.Height
	d.press(tea.WindowSizeMsg{Width: 60, Height: 12})
	short := d.model.summary.pane.Height
	if tall <= short || short < previewMinimumHeight {
		t.Errorf("pane heights %d then %d; want the pane to shrink with the terminal but never below %d", tall, short, previewMinimumHeight)
	}
	if d.model.summary.pane.Width != 56 {
		t.Errorf("pane width %d on a 60-column terminal, want 56", d.model.summary.pane.Width)
	}
}
