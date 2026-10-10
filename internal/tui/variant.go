package tui

import (
	"io"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"frostroot/internal/form"
)

// variantField is one question that each family, or each target, asks its
// own way on a page they all share, such as the release on the Image page:
// a widget per way of asking it, of which the answered variant's is the one
// shown, focused and given the keys. huh can hide a whole group but not one
// field of it, and a select whose options followed the family through huh's
// OptionsFunc would come back to Ubuntu from Fedora with its cursor on 20.04,
// the first option, rather than where it was. Each widget here keeps its own
// cursor and its own variable, so switching the family or the target and
// back finds the answer where it was left. Variants that ask the question
// the same way share a widget: Ubuntu's release is one question, whatever
// the target.
type variantField struct {
	key      string
	widgets  []huh.Field          // one per way of asking, in the order the fields are listed
	of       map[form.Variant]int // the widget each variant is shown
	answered func() form.Variant
}

// currentIndex is the widget of the variant answered so far. A variant the
// field has no widget for cannot be answered on a page every variant
// shares; the first widget stands in, so that the field is never without
// one.
func (f *variantField) currentIndex() int {
	if index, found := f.of[f.answered()]; found {
		return index
	}
	return 0
}

func (f *variantField) current() huh.Field { return f.widgets[f.currentIndex()] }

// each calls do with every widget, in order, and keeps what it returns.
func (f *variantField) each(do func(huh.Field) huh.Field) {
	for index := range f.widgets {
		f.widgets[index] = do(f.widgets[index])
	}
}

// Init implements huh.Field.
func (f *variantField) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(f.widgets))
	for _, widget := range f.widgets {
		cmds = append(cmds, widget.Init())
	}
	return tea.Batch(cmds...)
}

// Update gives a key to the answered variant's widget alone, and anything
// else to every widget, so that one shown after a switch is as up to date
// as the one it replaces.
func (f *variantField) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	current := f.currentIndex()
	_, isKey := msg.(tea.KeyMsg)
	var cmds []tea.Cmd
	for index, widget := range f.widgets {
		if isKey && index != current {
			continue
		}
		model, cmd := widget.Update(msg)
		if updated, isField := model.(huh.Field); isField {
			f.widgets[index] = updated
		}
		cmds = append(cmds, cmd)
	}
	return f, tea.Batch(cmds...)
}

// View implements huh.Field.
func (f *variantField) View() string { return f.current().View() }

// Blur implements huh.Field.
func (f *variantField) Blur() tea.Cmd { return f.current().Blur() }

// Focus implements huh.Field.
func (f *variantField) Focus() tea.Cmd { return f.current().Focus() }

// Error implements huh.Field.
func (f *variantField) Error() error { return f.current().Error() }

// Run implements huh.Field.
func (f *variantField) Run() error { return f.current().Run() }

// RunAccessible implements huh.Field.
func (f *variantField) RunAccessible(w io.Writer, r io.Reader) error {
	return f.current().RunAccessible(w, r)
}

// Skip implements huh.Field.
func (f *variantField) Skip() bool { return f.current().Skip() }

// Zoom implements huh.Field.
func (f *variantField) Zoom() bool { return f.current().Zoom() }

// KeyBinds implements huh.Field.
func (f *variantField) KeyBinds() []key.Binding { return f.current().KeyBinds() }

// WithTheme implements huh.Field.
func (f *variantField) WithTheme(theme *huh.Theme) huh.Field {
	f.each(func(widget huh.Field) huh.Field { return widget.WithTheme(theme) })
	return f
}

// WithAccessible implements huh.Field. huh's accessible mode is not used:
// the plain interface is frostroot's line-by-line one.
func (f *variantField) WithAccessible(bool) huh.Field { return f }

// WithKeyMap implements huh.Field.
func (f *variantField) WithKeyMap(keyMap *huh.KeyMap) huh.Field {
	f.each(func(widget huh.Field) huh.Field { return widget.WithKeyMap(keyMap) })
	return f
}

// WithWidth implements huh.Field.
func (f *variantField) WithWidth(width int) huh.Field {
	f.each(func(widget huh.Field) huh.Field { return widget.WithWidth(width) })
	return f
}

// WithHeight implements huh.Field.
func (f *variantField) WithHeight(height int) huh.Field {
	f.each(func(widget huh.Field) huh.Field { return widget.WithHeight(height) })
	return f
}

// WithPosition implements huh.Field.
func (f *variantField) WithPosition(position huh.FieldPosition) huh.Field {
	f.each(func(widget huh.Field) huh.Field { return widget.WithPosition(position) })
	return f
}

// GetKey implements huh.Field.
func (f *variantField) GetKey() string { return f.key }

// GetValue implements huh.Field.
func (f *variantField) GetValue() any { return f.current().GetValue() }
