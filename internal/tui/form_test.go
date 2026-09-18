package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"frostroot/internal/form"
)

// noHost is a form host that can read nothing, so lists come from the
// embedded data.
var noHost = form.Host{ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist }}

// maxKeyPresses bounds the loops that press Enter until a form finishes.
const maxKeyPresses = 40

// Key presses the tests send.
var (
	pressEnter = tea.KeyMsg{Type: tea.KeyEnter}
	pressUp    = tea.KeyMsg{Type: tea.KeyUp}
	pressLeft  = tea.KeyMsg{Type: tea.KeyLeft}
	pressEsc   = tea.KeyMsg{Type: tea.KeyEsc}
	pressCtrlC = tea.KeyMsg{Type: tea.KeyCtrlC}
)

func typeText(text string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)} }

// formDriver drives a formModel.
type formDriver struct {
	*driver
	model *formModel
}

func newFormDriver(t *testing.T, initial form.Values) *formDriver {
	t.Helper()
	return newFormDriverWithPreview(t, initial, nil)
}

// newFormDriverWithPreview drives a form whose last page shows preview.
func newFormDriverWithPreview(t *testing.T, initial form.Values, preview PreviewFunc) *formDriver {
	t.Helper()
	model := newFormModel(form.Fields(noHost), initial, preview)
	return &formDriver{driver: newDriver(t, model), model: model}
}

// pressEnterUntil presses Enter until the model reaches stage or the press
// budget runs out.
func (d *formDriver) pressEnterUntil(stage formStage) {
	d.t.Helper()
	for presses := 0; d.model.stage < stage && presses < maxKeyPresses; presses++ {
		d.press(pressEnter)
	}
	if d.model.stage < stage {
		d.t.Fatalf("still at stage %d after %d Enter presses", d.model.stage, maxKeyPresses)
	}
}

func TestFormBindingCoversEveryField(t *testing.T) {
	fields := form.Fields(noHost)
	binding := newFormBinding(fields, form.Defaults(noHost))
	if bound := len(binding.texts) + len(binding.flags) + len(binding.lists); bound != len(fields) {
		t.Errorf("%d fields bound, want %d", bound, len(fields))
	}
	for _, field := range fields {
		huhField := binding.huhField(field)
		if huhField.GetKey() != field.Key {
			t.Errorf("widget for %s has key %q", field.Key, huhField.GetKey())
		}
	}
	// One group per page that has a field: the Captured page has none
	// unless capture puts its note there.
	pagesWithFields := map[string]bool{}
	for _, field := range fields {
		pagesWithFields[field.Page] = true
	}
	if groups := binding.groups(); len(groups) != len(pagesWithFields) {
		t.Errorf("%d groups, want one per page with a field (%d)", len(groups), len(pagesWithFields))
	}
	// Untouched, the binding gives back what it started from.
	defaults := form.Defaults(noHost)
	if got := binding.values(); !reflect.DeepEqual(got, defaults) {
		t.Errorf("values() = %v, want the defaults %v", got, defaults)
	}
}

func TestFormModelAcceptsDefaults(t *testing.T) {
	d := newFormDriver(t, form.Defaults(noHost))
	d.pressEnterUntil(stageDone)
	if !d.quit || !d.model.write {
		t.Errorf("quit = %v, write = %v; want the program to quit with write confirmed", d.quit, d.model.write)
	}
	if got, want := form.ToRecipe(d.model.binding.values()), form.ToRecipe(form.Defaults(noHost)); !reflect.DeepEqual(got, want) {
		t.Errorf("accepting every default gave\n%+v\nwant\n%+v", got, want)
	}
}

func TestFormModelTakesTypedAndChosenAnswers(t *testing.T) {
	d := newFormDriver(t, form.Defaults(noHost))
	// Replace "lab" with "cpp-lab", then pick the release above the default.
	for range len("lab") {
		d.press(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	d.press(typeText("cpp-lab"))
	d.press(pressEnter)
	d.press(pressUp)
	d.pressEnterUntil(stageDone)
	values := d.model.binding.values()
	if got := values.String(form.KeyImageName); got != "cpp-lab" {
		t.Errorf("image name = %q, want cpp-lab", got)
	}
	if got := values.String(form.KeyRelease); got != "22.04" {
		t.Errorf("release = %q, want 22.04, one step up from 24.04", got)
	}
}

func TestFormModelShowsSummaryBeforeWriting(t *testing.T) {
	d := newFormDriver(t, form.Defaults(noHost))
	d.pressEnterUntil(stageSummary)
	view := d.model.View()
	for _, wantText := range []string{"Summary", "lab, Ubuntu 24.04 amd64", "student, passwordless sudo", "Write frostroot.toml?"} {
		if !strings.Contains(view, wantText) {
			t.Errorf("summary page lacks %q:\n%s", wantText, view)
		}
	}
}

func TestFormModelCancels(t *testing.T) {
	t.Run("Ctrl-C on the first page", func(t *testing.T) {
		d := newFormDriver(t, form.Defaults(noHost))
		d.press(pressCtrlC)
		if !d.interrupted {
			t.Error("Ctrl-C should interrupt the program")
		}
	})
	t.Run("Ctrl-C on a later page", func(t *testing.T) {
		d := newFormDriver(t, form.Defaults(noHost))
		d.press(pressEnter)
		d.press(pressEnter)
		d.press(pressCtrlC)
		if !d.interrupted {
			t.Error("Ctrl-C should interrupt the program")
		}
	})
	t.Run("Esc does not quit: it belongs to the filter", func(t *testing.T) {
		d := newFormDriver(t, form.Defaults(noHost))
		d.press(pressEsc)
		if d.interrupted || d.quit {
			t.Error("Esc must not end the form; only Ctrl-C does")
		}
	})
	t.Run("declined on the summary", func(t *testing.T) {
		d := newFormDriver(t, form.Defaults(noHost))
		d.pressEnterUntil(stageSummary)
		d.press(pressLeft) // from "Write" to "Cancel"
		d.press(pressEnter)
		if !d.quit || d.model.stage != stageDone || d.model.write {
			t.Errorf("quit = %v, stage = %d, write = %v; want finished without writing", d.quit, d.model.stage, d.model.write)
		}
	})
}

// pacedKeys returns a reader that delivers each key sequence as its own
// read, a moment apart, the way a person types: fast enough for a test, slow
// enough for the program to move focus between keys.
func pacedKeys(keys ...string) io.Reader {
	reader, writer := io.Pipe()
	go func() {
		for _, key := range keys {
			time.Sleep(30 * time.Millisecond)
			if _, err := io.WriteString(writer, key); err != nil {
				return // the program has stopped reading
			}
		}
		// Leave the pipe open: a closed input would end the program early.
	}()
	return reader
}

func TestRunFormEndToEnd(t *testing.T) {
	t.Run("defaults accepted with Enter", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		keys := make([]string, 0, maxKeyPresses)
		for range maxKeyPresses {
			keys = append(keys, "\r")
		}
		values, err := RunForm(ctx, form.Fields(noHost), form.Defaults(noHost), nil, pacedKeys(keys...), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := form.ToRecipe(values), form.ToRecipe(form.Defaults(noHost)); !reflect.DeepEqual(got, want) {
			t.Errorf("accepting every default gave\n%+v\nwant\n%+v", got, want)
		}
	})
	t.Run("Ctrl-C cancels", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := RunForm(ctx, form.Fields(noHost), form.Defaults(noHost), nil, pacedKeys("\r", "\x03"), io.Discard)
		if !errors.Is(err, ErrCanceled) {
			t.Errorf("err = %v, want ErrCanceled", err)
		}
	})
}
