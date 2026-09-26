package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

// TestSummaryPageFitsTheWidth: resize counts the summary and the warning as
// one row per line, and View wrote them whole, so a capture's answer of
// hundreds of packages on one line wrapped over rows the arithmetic did not
// count, and on a 60-column terminal the heading, the answers and the
// question scrolled off the one screen that asks whether to write. Every
// line is cut to the width now, as the pane's already were.
func TestSummaryPageFitsTheWidth(t *testing.T) {
	values := form.Defaults(noHost)
	values[form.KeyOtherPackages] = strings.TrimSpace(strings.Repeat("libexample-dev ", 40))
	warning := "Not in the archive: " + strings.TrimSpace(strings.Repeat("libexample-dev ", 40))
	preview := func(form.Values) Preview {
		return Preview{Heading: "This is what frostroot.toml will say:", Text: "[image]\nname = \"lab\"\n", Warning: warning}
	}
	d := newFormDriverWithPreview(t, values, preview)
	d.press(tea.WindowSizeMsg{Width: 60, Height: 20})
	d.pressEnterUntil(stageSummary)
	view := frameOf(d.model.View())
	for number, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
		if width := lipgloss.Width(line); width > 60 {
			t.Errorf("line %d is %d cells wide on a 60-column terminal: %q", number+1, width, line)
		}
	}
	for _, wantText := range []string{"Summary", "libexample-dev", "Not in the archive", "Write frostroot.toml?"} {
		if !strings.Contains(view, wantText) {
			t.Errorf("summary page lacks %q:\n%s", wantText, view)
		}
	}
}
