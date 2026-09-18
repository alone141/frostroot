package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"frostroot/internal/form"
)

// captureNote is what capture puts on its first page, underscores and all.
const captureNote = `Read /: Ubuntu 24.04, 1893 packages installed, 41 asked for, 2 third-party sources with keys.

A recipe cannot carry:
  pip packages outside a venv          3
  snap packages                        nothing found
  files under /etc changed by hand     12

The details go to frostroot-capture.md, written beside the recipe.`

func TestANoteOpensTheFormAndAsksNothing(t *testing.T) {
	fields := slices.Concat([]form.Field{form.NoteField(form.PageCaptured, "What capture found", captureNote)}, form.Fields(noHost))
	model := newFormModel(context.Background(), fields, form.Defaults(noHost), nil)
	d := &formDriver{driver: newDriver(t, model), model: model}
	d.press(tea.WindowSizeMsg{Width: 100, Height: 40})

	view := frameOf(d.model.View())
	for _, wantText := range []string{"What capture found", "files under /etc changed by hand", "frostroot-capture.md", "Continue"} {
		if !strings.Contains(view, wantText) {
			t.Errorf("the first page lacks %q:\n%s", wantText, view)
		}
	}
	if strings.Contains(view, "Image name") {
		t.Errorf("the note shares its page with a question:\n%s", view)
	}

	d.press(pressEnter)
	if next := frameOf(d.model.View()); !strings.Contains(next, "Image name") {
		t.Errorf("Enter on the note did not move to the first question:\n%s", next)
	}
	// The note left nothing in the answers.
	if _, bound := d.model.binding.texts["note:"+form.PageCaptured]; bound {
		t.Error("a note was bound as if it were answered")
	}
	d.pressEnterUntil(stageDone)
	if !d.quit || !d.model.write {
		t.Errorf("quit = %v, write = %v", d.quit, d.model.write)
	}
}

func TestCaptureNoteFrame(t *testing.T) {
	fields := slices.Concat([]form.Field{form.NoteField(form.PageCaptured, "What capture found", captureNote)}, form.Fields(noHost))
	model := newFormModel(context.Background(), fields, form.Defaults(noHost), nil)
	d := &formDriver{driver: newDriver(t, model), model: model}
	d.press(tea.WindowSizeMsg{Width: 80, Height: 24})
	assertFrame(t, frameName("form-capture-note", 80, 24), d.model.View())
}
